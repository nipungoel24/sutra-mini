package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func integrationServer(t *testing.T, issued int) (*server, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required; these tests exercise real Postgres concurrency")
	}
	// Setup truncates every table, so never let it point at the runtime database.
	if dsn == os.Getenv("DATABASE_URL") {
		t.Fatal("TEST_DATABASE_URL must not equal DATABASE_URL: integration tests truncate tables")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 30
	cfg.MinConns = 5
	db, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	_, err = db.Exec(context.Background(), "TRUNCATE vouchers, jobwork_receipts, jobwork_issues, parties, skus CASCADE")
	if err != nil {
		t.Fatalf("database must have migrations applied: %v", err)
	}
	var party, sku, issue string
	if err := db.QueryRow(context.Background(), "INSERT INTO parties(name,phone) VALUES('test worker','1') RETURNING id").Scan(&party); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(context.Background(), "INSERT INTO skus(code,name,image_url) VALUES('T-1','Test SKU','https://example.com/x') RETURNING id").Scan(&sku); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(context.Background(), "INSERT INTO jobwork_issues(party_id,sku_id,qty,issued_on) VALUES($1,$2,$3,current_date) RETURNING id", party, sku, issued).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	return &server{db: db}, issue
}

func postReceipt(t *testing.T, s *server, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/jobwork/receipts", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	rr := httptest.NewRecorder()
	s.createReceipt(rr, req)
	return rr
}

func callReceiptBody(t *testing.T, s *server, issue, key string) (int, receiptResponse) {
	body, _ := json.Marshal(receiptRequest{IssueID: issue, Qty: 1, ReceivedOn: time.Now().UTC()})
	rr := postReceipt(t, s, key, string(body))
	if rr.Code != http.StatusOK && rr.Code != http.StatusCreated && rr.Code != http.StatusConflict {
		t.Logf("callReceipt key=%s status=%d body=%s", key, rr.Code, rr.Body.String())
	}
	var out receiptResponse
	_ = json.NewDecoder(bytes.NewReader(rr.Body.Bytes())).Decode(&out)
	return rr.Code, out
}

func callReceipt(t *testing.T, s *server, issue, key string) int {
	code, _ := callReceiptBody(t, s, issue, key)
	return code
}

func TestConcurrentReceiptsCannotOvershoot(t *testing.T) {
	s, issue := integrationServer(t, 5)
	var wg sync.WaitGroup
	codes := make(chan int, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes <- callReceipt(t, s, issue, "concurrent-"+string(rune('a'+i)))
		}(i)
	}
	wg.Wait()
	close(codes)
	created := 0
	for code := range codes {
		if code == http.StatusCreated {
			created++
		} else if code != http.StatusConflict {
			t.Errorf("unexpected status %d", code)
		}
	}
	if created != 5 {
		t.Fatalf("created %d receipts, want 5", created)
	}
	var total, receipts, vouchers int
	if err := s.db.QueryRow(context.Background(), "SELECT COALESCE(SUM(qty),0), count(*) FROM jobwork_receipts WHERE issue_id=$1", issue).Scan(&total, &receipts); err != nil {
		t.Fatal(err)
	}
	if total != 5 {
		t.Fatalf("received %d, want issued quantity 5", total)
	}
	// Rejected requests must leave nothing behind, and every accepted receipt has exactly one voucher.
	if err := s.db.QueryRow(context.Background(), "SELECT count(*) FROM vouchers v JOIN jobwork_receipts r ON r.id=v.source_id AND v.type='jobwork_receipt'").Scan(&vouchers); err != nil {
		t.Fatal(err)
	}
	var allVouchers int
	if err := s.db.QueryRow(context.Background(), "SELECT count(*) FROM vouchers").Scan(&allVouchers); err != nil {
		t.Fatal(err)
	}
	if receipts != 5 || vouchers != 5 || allVouchers != 5 {
		t.Fatalf("got %d receipts, %d matched vouchers, %d total vouchers; want 5 each", receipts, vouchers, allVouchers)
	}
}

func TestIdempotencyReplayCreatesNothingTwice(t *testing.T) {
	s, issue := integrationServer(t, 5)
	got, first := callReceiptBody(t, s, issue, "same-key")
	if got != http.StatusCreated {
		t.Fatalf("first request: %d", got)
	}
	got, replay := callReceiptBody(t, s, issue, "same-key")
	if got != http.StatusOK {
		t.Fatalf("replay: %d", got)
	}
	if first.ID == "" || replay.ID != first.ID || replay.VoucherID != first.VoucherID {
		t.Fatalf("replay returned receipt %q voucher %q; want original receipt %q voucher %q", replay.ID, replay.VoucherID, first.ID, first.VoucherID)
	}
	var receipts, vouchers int
	if err := s.db.QueryRow(context.Background(), "SELECT count(*) FROM jobwork_receipts").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(context.Background(), "SELECT count(*) FROM vouchers").Scan(&vouchers); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || vouchers != 1 {
		t.Fatalf("got %d receipts and %d vouchers; want one each", receipts, vouchers)
	}
}

func TestReceiptRejectsInvalidInputWithoutLeakingSQL(t *testing.T) {
	s, issue := integrationServer(t, 5)
	cases := []struct {
		name, key, body string
		want            int
	}{
		{"malformed issue uuid", "k1", `{"issue_id":"not-a-uuid","qty":1,"received_on":"2026-10-12T00:00:00Z"}`, http.StatusBadRequest},
		{"unknown issue", "k2", `{"issue_id":"00000000-0000-0000-0000-000000000000","qty":1,"received_on":"2026-10-12T00:00:00Z"}`, http.StatusNotFound},
		{"zero qty", "k3", `{"issue_id":"` + issue + `","qty":0,"received_on":"2026-10-12T00:00:00Z"}`, http.StatusBadRequest},
		{"malformed json", "k4", `{"issue_id":`, http.StatusBadRequest},
		{"blank idempotency key", "   ", `{"issue_id":"` + issue + `","qty":1,"received_on":"2026-10-12T00:00:00Z"}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		rr := postReceipt(t, s, c.key, c.body)
		if rr.Code != c.want {
			t.Errorf("%s: status %d, want %d (body %s)", c.name, rr.Code, c.want, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "SQLSTATE") {
			t.Errorf("%s: response leaks database error: %s", c.name, rr.Body.String())
		}
	}
	var receipts int
	if err := s.db.QueryRow(context.Background(), "SELECT count(*) FROM jobwork_receipts").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 {
		t.Fatalf("invalid requests created %d receipts", receipts)
	}
}
