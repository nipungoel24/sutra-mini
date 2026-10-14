package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type server struct{ db *pgxpool.Pool }
type receiptRequest struct {
	IssueID    string    `json:"issue_id"`
	Qty        int       `json:"qty"`
	ReceivedOn time.Time `json:"received_on"`
}
type receiptResponse struct {
	ID         string    `json:"id"`
	IssueID    string    `json:"issue_id"`
	VoucherID  string    `json:"voucher_id"`
	Status     string    `json:"voucher_status"`
	Qty        int       `json:"qty"`
	ReceivedOn time.Time `json:"received_on"`
}

// uuidRE rejects malformed ids before they reach Postgres, so callers get a 4xx
// instead of a 500 carrying a SQLSTATE 22P02 message.
var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const maxBodyBytes = 1 << 20

func main() {
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(requiredEnv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	cfg.MaxConns = 30
	cfg.MinConns = 5
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	s := &server{db: db}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /jobwork/receipts", s.createReceipt)
	mux.HandleFunc("GET /issues/{id}", s.getIssue)
	mux.HandleFunc("GET /vouchers", s.listVouchers)
	mux.HandleFunc("POST /vouchers/{id}/retry", s.retryVoucher)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	addr := env("API_ADDR", ":8080")
	log.Printf("api listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, cors(mux)))
}
func (s *server) createReceipt(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, 400, "Idempotency-Key is required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var in receiptRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.IssueID == "" || in.Qty <= 0 || in.ReceivedOn.IsZero() {
		writeError(w, 400, "issue_id, positive qty, and received_on (RFC 3339) are required")
		return
	}
	if !uuidRE.MatchString(in.IssueID) {
		writeError(w, 400, "issue_id must be a UUID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		log.Printf("begin tx error: %v", err)
		writeError(w, 500, "database unavailable")
		return
	}
	defer tx.Rollback(ctx)
	// Check idempotency before waiting for the issue lock
	var out receiptResponse
	err = tx.QueryRow(ctx, `SELECT r.id, r.issue_id, r.qty, r.received_on, v.id, v.status
    FROM jobwork_receipts r JOIN vouchers v ON v.source_id=r.id AND v.type='jobwork_receipt' WHERE r.idempotency_key=$1`, key).Scan(&out.ID, &out.IssueID, &out.Qty, &out.ReceivedOn, &out.VoucherID, &out.Status)
	if err == nil {
		tx.Commit(ctx)
		writeJSON(w, http.StatusOK, out)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		log.Printf("idempotency check error: %v", err)
		writeError(w, 500, "read idempotency record")
		return
	}
	var issued int
	if err = tx.QueryRow(ctx, `SELECT qty FROM jobwork_issues WHERE id=$1::uuid FOR UPDATE`, in.IssueID).Scan(&issued); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "issue not found")
			return
		}
		log.Printf("issue lock error: %v", err)
		writeError(w, 500, "lock issue")
		return
	}
	// Recheck after acquiring the issue lock. A same-key request can have arrived
	// while this transaction was waiting for another receipt on this issue.
	err = tx.QueryRow(ctx, `SELECT r.id, r.issue_id, r.qty, r.received_on, v.id, v.status
    FROM jobwork_receipts r JOIN vouchers v ON v.source_id=r.id AND v.type='jobwork_receipt' WHERE r.idempotency_key=$1`, key).Scan(&out.ID, &out.IssueID, &out.Qty, &out.ReceivedOn, &out.VoucherID, &out.Status)
	if err == nil {
		tx.Commit(ctx)
		writeJSON(w, http.StatusOK, out)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		log.Printf("recheck idempotency error: %v", err)
		writeError(w, 500, "read idempotency record")
		return
	}
	var received int
	if err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(qty),0) FROM jobwork_receipts WHERE issue_id=$1::uuid`, in.IssueID).Scan(&received); err != nil {
		log.Printf("read receipts error: %v", err)
		writeError(w, 500, "read receipts")
		return
	}
	if received+in.Qty > issued {
		writeError(w, 409, "receipt would exceed issued quantity")
		return
	}
	err = tx.QueryRow(ctx, `INSERT INTO jobwork_receipts(issue_id,qty,received_on,idempotency_key) VALUES($1::uuid,$2,$3,$4) RETURNING id, issue_id, qty, received_on`, in.IssueID, in.Qty, in.ReceivedOn, key).Scan(&out.ID, &out.IssueID, &out.Qty, &out.ReceivedOn)
	if err != nil {
		// Same key raced in on a different issue (so it never waited on our issue lock): the UNIQUE index is the final guard.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, 409, "idempotency key already used by a concurrent request")
			return
		}
		log.Printf("create receipt error: %v", err)
		writeError(w, 500, "create receipt")
		return
	}
	// Amount is deliberately zero: the provided schema has no rate. This voucher records the accounting event safely.
	err = tx.QueryRow(ctx, `INSERT INTO vouchers(type,source_id,amount_paise,status,client_ref) VALUES('jobwork_receipt',$1::uuid,0,'pending','receipt:' || $1::text) RETURNING id,status`, out.ID).Scan(&out.VoucherID, &out.Status)
	if err != nil {
		log.Printf("create voucher error: %v", err)
		writeError(w, 500, "create voucher")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		log.Printf("commit receipt error: %v", err)
		writeError(w, 500, "commit receipt")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
func (s *server) getIssue(w http.ResponseWriter, r *http.Request) {
	if !uuidRE.MatchString(r.PathValue("id")) {
		writeError(w, 404, "issue not found")
		return
	}
	var x struct {
		ID          string `json:"id"`
		Issued      int    `json:"issued"`
		Received    int    `json:"received"`
		Outstanding int    `json:"outstanding"`
	}
	err := s.db.QueryRow(r.Context(), `SELECT i.id,i.qty,COALESCE(SUM(r.qty),0),i.qty-COALESCE(SUM(r.qty),0) FROM jobwork_issues i LEFT JOIN jobwork_receipts r ON r.issue_id=i.id WHERE i.id=$1::uuid GROUP BY i.id`, r.PathValue("id")).Scan(&x.ID, &x.Issued, &x.Received, &x.Outstanding)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "issue not found")
		} else {
			log.Printf("get issue error: %v", err)
			writeError(w, 500, "database error")
		}
		return
	}
	writeJSON(w, 200, x)
}
func (s *server) listVouchers(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	rows, err := s.db.Query(r.Context(), `SELECT id,type,source_id,amount_paise,status,attempts,COALESCE(last_error,''),client_ref,COALESCE(external_ref,''),created_at,updated_at FROM vouchers WHERE ($1='' OR status=$1) ORDER BY created_at DESC`, status)
	if err != nil {
		log.Printf("list vouchers error: %v", err)
		writeError(w, 500, "list vouchers")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, typ, source, st, errText, ref, external string
		var amt int64
		var attempts int
		var created, updated time.Time
		if err := rows.Scan(&id, &typ, &source, &amt, &st, &attempts, &errText, &ref, &external, &created, &updated); err != nil {
			log.Printf("scan voucher error: %v", err)
			writeError(w, 500, "list vouchers")
			return
		}
		out = append(out, map[string]any{"id": id, "type": typ, "source_id": source, "amount_paise": amt, "status": st, "attempts": attempts, "last_error": errText, "client_ref": ref, "external_ref": external, "created_at": created, "updated_at": updated})
	}
	if err := rows.Err(); err != nil {
		log.Printf("list vouchers rows error: %v", err)
		writeError(w, 500, "list vouchers")
		return
	}
	writeJSON(w, 200, out)
}
func (s *server) retryVoucher(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidRE.MatchString(id) {
		writeError(w, 404, "voucher not found")
		return
	}
	tag, err := s.db.Exec(r.Context(), `UPDATE vouchers SET status='pending',last_error=NULL,next_attempt_at=now(),locked_until=NULL,updated_at=now() WHERE id=$1::uuid AND status='failed'`, id)
	if err != nil {
		log.Printf("retry voucher error: %v", err)
		writeError(w, 500, "retry voucher")
		return
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM vouchers WHERE id=$1::uuid)`, id).Scan(&exists); err == nil && !exists {
			writeError(w, 404, "voucher not found")
			return
		}
		writeError(w, 409, "only failed vouchers can be retried")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "pending"})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
func requiredEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("%s is required", k)
	}
	return v
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Local demo default is "*" (no cookies/credentials are used); set CORS_ORIGIN to pin the dashboard origin.
		w.Header().Set("Access-Control-Allow-Origin", env("CORS_ORIGIN", "*"))
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}
