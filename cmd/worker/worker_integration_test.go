package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for database integration tests")
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 20
	cfg.MinConns = 2

	db, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

	_, err = db.Exec(context.Background(), "TRUNCATE vouchers CASCADE")
	if err != nil {
		t.Fatalf("truncate vouchers: %v", err)
	}

	return db
}

// TestWorkerDuplicateReconciliation verifies that when accounting returns 409
// for a duplicate client_ref, the worker safely reconciles and marks the voucher pushed.
func TestWorkerDuplicateReconciliation(t *testing.T) {
	db := setupTestDB(t)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"external_ref": "acct-reconciled-409",
		})
	}))
	defer mockServer.Close()

	ctx := context.Background()
	var voucherID string
	err := db.QueryRow(ctx, `
		INSERT INTO vouchers(type, source_id, amount_paise, status, client_ref)
		VALUES('jobwork_receipt', gen_random_uuid(), 0, 'pending', 'receipt:test-recon-1')
		RETURNING id`).Scan(&voucherID)
	if err != nil {
		t.Fatalf("insert voucher: %v", err)
	}

	// Drain once
	if err := drain(ctx, db, mockServer.URL); err != nil {
		t.Fatalf("drain error: %v", err)
	}

	// Wait for async push to finish
	var status, extRef string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		err = db.QueryRow(ctx, `SELECT status, COALESCE(external_ref, '') FROM vouchers WHERE id=$1::uuid`, voucherID).Scan(&status, &extRef)
		if err == nil && status == "pushed" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if status != "pushed" {
		t.Fatalf("expected status 'pushed', got '%s'", status)
	}
	if extRef != "acct-reconciled-409" {
		t.Fatalf("expected external_ref 'acct-reconciled-409', got '%s'", extRef)
	}
}

// TestWorkerLeaseReclaimAfterCrash verifies that an expired lease (e.g. from a crashed worker)
// is safely reclaimed and processed by another worker run.
func TestWorkerLeaseReclaimAfterCrash(t *testing.T) {
	db := setupTestDB(t)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"external_ref": "acct-recovered-lease",
		})
	}))
	defer mockServer.Close()

	ctx := context.Background()
	var voucherID string
	// Insert voucher simulating a crashed worker: locked_until was in the past
	err := db.QueryRow(ctx, `
		INSERT INTO vouchers(type, source_id, amount_paise, status, attempts, locked_until, client_ref)
		VALUES('jobwork_receipt', gen_random_uuid(), 0, 'pending', 1, now() - interval '5 seconds', 'receipt:test-crash-1')
		RETURNING id`).Scan(&voucherID)
	if err != nil {
		t.Fatalf("insert voucher: %v", err)
	}

	if err := drain(ctx, db, mockServer.URL); err != nil {
		t.Fatalf("drain error: %v", err)
	}

	var status, extRef string
	var attempts int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		err = db.QueryRow(ctx, `SELECT status, COALESCE(external_ref, ''), attempts FROM vouchers WHERE id=$1::uuid`, voucherID).Scan(&status, &extRef, &attempts)
		if err == nil && status == "pushed" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if status != "pushed" {
		t.Fatalf("expected status 'pushed', got '%s'", status)
	}
	if extRef != "acct-recovered-lease" {
		t.Fatalf("expected external_ref 'acct-recovered-lease', got '%s'", extRef)
	}
	if attempts != 2 {
		t.Fatalf("expected attempts 2, got %d", attempts)
	}
}

// TestWorkerExhaustionToFailed verifies that repeated failures eventually mark
// the voucher as failed with the failure reason preserved.
func TestWorkerExhaustionToFailed(t *testing.T) {
	db := setupTestDB(t)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "fatal remote service error"})
	}))
	defer mockServer.Close()

	ctx := context.Background()
	var voucherID string
	// Insert voucher at attempt 5 (next claim will be attempt 6 -> exhausted)
	err := db.QueryRow(ctx, `
		INSERT INTO vouchers(type, source_id, amount_paise, status, attempts, client_ref)
		VALUES('jobwork_receipt', gen_random_uuid(), 0, 'pending', 5, 'receipt:test-exhaust-1')
		RETURNING id`).Scan(&voucherID)
	if err != nil {
		t.Fatalf("insert voucher: %v", err)
	}

	if err := drain(ctx, db, mockServer.URL); err != nil {
		t.Fatalf("drain error: %v", err)
	}

	var status, lastError string
	var attempts int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		err = db.QueryRow(ctx, `SELECT status, COALESCE(last_error, ''), attempts FROM vouchers WHERE id=$1::uuid`, voucherID).Scan(&status, &lastError, &attempts)
		if err == nil && status == "failed" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if status != "failed" {
		t.Fatalf("expected status 'failed', got '%s'", status)
	}
	if attempts != 6 {
		t.Fatalf("expected attempts 6, got %d", attempts)
	}
	if lastError == "" {
		t.Fatalf("expected non-empty last_error, got empty")
	}
}

// TestTwoWorkersConcurrentProcessing verifies that two concurrent worker instances
// can drain a queue together without claiming the same voucher or interfering.
func TestTwoWorkersConcurrentProcessing(t *testing.T) {
	db := setupTestDB(t)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"external_ref": "acct-" + r.URL.Query().Get("x"),
		})
	}))
	defer mockServer.Close()

	ctx := context.Background()
	for i := 0; i < 6; i++ {
		clientRef := fmt.Sprintf("receipt:test-worker-concur-%d", i)
		_, err := db.Exec(ctx, `
			INSERT INTO vouchers(type, source_id, amount_paise, status, client_ref)
			VALUES('jobwork_receipt', gen_random_uuid(), 0, 'pending', $1)`, clientRef)
		if err != nil {
			t.Fatalf("insert voucher %d: %v", i, err)
		}
	}

	// Run two worker drain operations concurrently
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = drain(ctx, db, mockServer.URL)
	}()
	go func() {
		defer wg.Done()
		_ = drain(ctx, db, mockServer.URL)
	}()
	wg.Wait()

	var pendingCount, pushedCount int
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_ = db.QueryRow(ctx, `SELECT count(*) FROM vouchers WHERE status='pending'`).Scan(&pendingCount)
		_ = db.QueryRow(ctx, `SELECT count(*) FROM vouchers WHERE status='pushed'`).Scan(&pushedCount)
		if pushedCount == 6 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	if pushedCount != 6 {
		t.Fatalf("expected all 6 vouchers to be pushed, got pushed=%d, pending=%d", pushedCount, pendingCount)
	}
}
