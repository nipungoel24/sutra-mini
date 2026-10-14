package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type voucher struct {
	ID        string
	ClientRef string
	Amount    int64
	Attempts  int
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		log.Fatalf("parse db config: %v", err)
	}
	cfg.MaxConns = 20
	cfg.MinConns = 2

	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer db.Close()

	accountingURL := os.Getenv("ACCOUNTING_URL")
	if accountingURL == "" {
		accountingURL = "http://localhost:8081"
	}

	log.Printf("[worker] Voucher sync worker started (accounting: %s)", accountingURL)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[worker] Shutting down worker...")
			return
		case <-ticker.C:
			if err := drain(ctx, db, accountingURL); err != nil {
				log.Printf("[worker] drain error: %v", err)
			}
		}
	}
}

func drain(ctx context.Context, db *pgxpool.Pool, accountingURL string) error {
	for i := 0; i < 8; i++ {
		var v voucher
		// Atomically claim one eligible pending voucher using FOR UPDATE SKIP LOCKED
		query := `
			WITH claimed AS (
				SELECT id
				FROM vouchers
				WHERE status = 'pending'
				  AND next_attempt_at <= now()
				  AND (locked_until IS NULL OR locked_until < now())
				ORDER BY created_at
				FOR UPDATE SKIP LOCKED
				LIMIT 1
			)
			UPDATE vouchers v
			SET attempts = v.attempts + 1,
			    locked_until = now() + interval '15 seconds',
			    updated_at = now()
			FROM claimed
			WHERE v.id = claimed.id
			RETURNING v.id, v.client_ref, v.amount_paise, v.attempts`

		err := db.QueryRow(ctx, query).Scan(&v.ID, &v.ClientRef, &v.Amount, &v.Attempts)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("claim voucher: %w", err)
		}

		log.Printf("[worker] Claimed voucher %s (client_ref: %s, attempt: %d)", v.ID, v.ClientRef, v.Attempts)
		go push(context.Background(), db, accountingURL, v)
	}
	return nil
}

func push(ctx context.Context, db *pgxpool.Pool, accountingURL string, v voucher) {
	reqBody, _ := json.Marshal(map[string]any{
		"client_ref":   v.ClientRef,
		"amount_paise": v.Amount,
	})

	// Explicit 2-second timeout on outbound HTTP call
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodPost, accountingURL+"/api/vouchers", bytes.NewReader(reqBody))
	if err != nil {
		handleFailure(ctx, db, v, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 2500 * time.Millisecond}
	res, err := client.Do(req)

	if err != nil {
		log.Printf("[worker] HTTP error for %s (attempt %d): %v", v.ClientRef, v.Attempts, err)
		handleFailure(ctx, db, v, err.Error())
		return
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusOK || res.StatusCode == http.StatusCreated {
		var resp struct {
			ExternalRef string `json:"external_ref"`
		}
		if err := json.NewDecoder(res.Body).Decode(&resp); err == nil && resp.ExternalRef != "" {
			log.Printf("[worker] Success pushing %s -> external_ref: %s", v.ClientRef, resp.ExternalRef)
			markPushed(ctx, db, v, resp.ExternalRef)
			return
		}
		handleFailure(ctx, db, v, "invalid success response payload from accounting")
		return
	}

	if res.StatusCode == http.StatusConflict {
		// HTTP 409 duplicate reconciliation:
		// Remote already processed this client_ref. Extract and verify the original external_ref.
		var resp struct {
			ExternalRef string `json:"external_ref"`
		}
		if err := json.NewDecoder(res.Body).Decode(&resp); err == nil && resp.ExternalRef != "" {
			log.Printf("[worker] Reconciled duplicate 409 for %s -> original external_ref: %s", v.ClientRef, resp.ExternalRef)
			markPushed(ctx, db, v, resp.ExternalRef)
			return
		}
		handleFailure(ctx, db, v, "accounting returned 409 without valid external_ref")
		return
	}

	// Any other HTTP response (e.g. 500)
	var errResp struct {
		Error string `json:"error"`
	}
	errText := res.Status
	if err := json.NewDecoder(res.Body).Decode(&errResp); err == nil && errResp.Error != "" {
		errText = fmt.Sprintf("%s (%s)", res.Status, errResp.Error)
	}
	log.Printf("[worker] Non-success response for %s: %s", v.ClientRef, errText)
	handleFailure(ctx, db, v, errText)
}

// All result writes are fenced on (status='pending', attempts=<our claim>). Every claim
// increments attempts, so if our lease expired and another worker reclaimed the voucher,
// our late write matches zero rows instead of overwriting the newer state.
func markPushed(ctx context.Context, db *pgxpool.Pool, v voucher, externalRef string) {
	tag, err := db.Exec(ctx, `UPDATE vouchers SET status='pushed', external_ref=$2, last_error=NULL, locked_until=NULL, updated_at=now() WHERE id=$1::uuid AND status='pending' AND attempts=$3`, v.ID, externalRef, v.Attempts)
	logResultWrite(v, "mark pushed", tag, err)
}

func logResultWrite(v voucher, action string, tag pgconn.CommandTag, err error) {
	if err != nil {
		log.Printf("[worker] Failed to %s voucher %s: %v", action, v.ID, err)
	} else if tag.RowsAffected() == 0 {
		log.Printf("[worker] Discarded stale result for %s (attempt %d): voucher was reclaimed or already settled", v.ClientRef, v.Attempts)
	}
}

func handleFailure(ctx context.Context, db *pgxpool.Pool, v voucher, errText string) {
	if v.Attempts >= 6 {
		log.Printf("[worker] Voucher %s exhausted %d attempts -> marking failed: %s", v.ClientRef, v.Attempts, errText)
		tag, err := db.Exec(ctx, `UPDATE vouchers SET status='failed', last_error=$2, locked_until=NULL, updated_at=now() WHERE id=$1::uuid AND status='pending' AND attempts=$3`, v.ID, errText, v.Attempts)
		logResultWrite(v, "mark failed", tag, err)
		return
	}

	// Exponential backoff: 2s, 4s, 8s, 16s, 32s, 64s
	delaySeconds := 1 << min(v.Attempts, 6)
	intervalStr := fmt.Sprintf("%d seconds", delaySeconds)

	log.Printf("[worker] Retrying %s in %s (attempt %d/6): %s", v.ClientRef, intervalStr, v.Attempts, errText)
	tag, err := db.Exec(ctx, `UPDATE vouchers SET next_attempt_at=now() + $2::interval, last_error=$3, locked_until=NULL, updated_at=now() WHERE id=$1::uuid AND status='pending' AND attempts=$4`, v.ID, intervalStr, errText, v.Attempts)
	logResultWrite(v, "schedule retry for", tag, err)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
