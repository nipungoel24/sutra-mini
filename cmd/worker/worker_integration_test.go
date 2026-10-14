package main

import (
	"bytes"
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
	// Setup truncates vouchers, so never let it point at the runtime database.
	if dsn == os.Getenv("DATABASE_URL") {
		t.Fatal("TEST_DATABASE_URL must not equal DATABASE_URL: integration tests truncate tables")
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

	// Count deliveries per client_ref so a double claim is detected even though
	// both deliveries would still leave the voucher "pushed".
	var mu sync.Mutex
	calls := map[string]int{}
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ClientRef string `json:"client_ref"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		mu.Lock()
		calls[in.ClientRef]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"external_ref": "acct-" + in.ClientRef,
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
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 6 {
		t.Fatalf("expected deliveries for 6 distinct client_refs, got %d: %v", len(calls), calls)
	}
	for ref, n := range calls {
		if n != 1 {
			t.Fatalf("client_ref %s was delivered %d times; two workers claimed the same voucher", ref, n)
		}
	}
	var maxAttempts int
	if err := db.QueryRow(ctx, `SELECT max(attempts) FROM vouchers`).Scan(&maxAttempts); err != nil {
		t.Fatal(err)
	}
	if maxAttempts != 1 {
		t.Fatalf("expected every voucher claimed exactly once, max attempts=%d", maxAttempts)
	}
}

// TestStaleWorkerResultIsDiscarded verifies the fencing on result writes: a worker whose
// lease expired (and whose voucher was reclaimed, bumping attempts) cannot overwrite the
// newer state when its late result finally arrives.
func TestStaleWorkerResultIsDiscarded(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	var pushedID string
	// Worker B reclaimed at attempt 7 and already settled the voucher as pushed.
	err := db.QueryRow(ctx, `
		INSERT INTO vouchers(type, source_id, amount_paise, status, attempts, client_ref, external_ref)
		VALUES('jobwork_receipt', gen_random_uuid(), 0, 'pushed', 7, 'receipt:test-stale-pushed', 'acct-from-b')
		RETURNING id`).Scan(&pushedID)
	if err != nil {
		t.Fatalf("insert pushed voucher: %v", err)
	}
	// Stale worker A still holds its attempt-6 claim and now reports exhaustion.
	handleFailure(ctx, db, voucher{ID: pushedID, ClientRef: "receipt:test-stale-pushed", Attempts: 6}, "stale timeout")

	var status, extRef, lastErr string
	if err := db.QueryRow(ctx, `SELECT status, COALESCE(external_ref,''), COALESCE(last_error,'') FROM vouchers WHERE id=$1::uuid`, pushedID).Scan(&status, &extRef, &lastErr); err != nil {
		t.Fatal(err)
	}
	if status != "pushed" || extRef != "acct-from-b" || lastErr != "" {
		t.Fatalf("stale failure overwrote settled voucher: status=%s external_ref=%s last_error=%q", status, extRef, lastErr)
	}

	var pendingID string
	// Worker B reclaimed at attempt 3 and is still in flight.
	err = db.QueryRow(ctx, `
		INSERT INTO vouchers(type, source_id, amount_paise, status, attempts, locked_until, client_ref)
		VALUES('jobwork_receipt', gen_random_uuid(), 0, 'pending', 3, now() + interval '15 seconds', 'receipt:test-stale-pending')
		RETURNING id`).Scan(&pendingID)
	if err != nil {
		t.Fatalf("insert pending voucher: %v", err)
	}
	// Stale worker A (attempt 2) schedules a retry; it must not clear B's lease.
	handleFailure(ctx, db, voucher{ID: pendingID, ClientRef: "receipt:test-stale-pending", Attempts: 2}, "stale 500")

	var leased bool
	if err := db.QueryRow(ctx, `SELECT locked_until > now(), COALESCE(last_error,'') FROM vouchers WHERE id=$1::uuid`, pendingID).Scan(&leased, &lastErr); err != nil {
		t.Fatal(err)
	}
	if !leased || lastErr != "" {
		t.Fatalf("stale retry write touched reclaimed voucher: leased=%v last_error=%q", leased, lastErr)
	}
}

// TestWorkerCrashMidFlightReconcilesDuplicate deterministically simulates the exact failure sequence:
// 1. A pending voucher exists in the database.
// 2. Worker 1 claims the voucher and successfully calls the remote accounting service.
// 3. Remote accounting service records the voucher and issues external_ref "acct-crash-sequence-1".
// 4. Worker 1 crashes BEFORE updating PostgreSQL (leaving the voucher pending with an active lease).
// 5. The lease expires (locked_until passes).
// 6. Worker 2 claims the voucher on retry and resends the same stable client_ref.
// 7. Accounting service detects duplicate client_ref, creates no duplicate, and returns HTTP 409 with original external_ref.
// 8. Worker 2 reconciles the 409 and marks the voucher pushed.
// 9. PostgreSQL verifies status='pushed' with the original external_ref and attempts=2.
// 10. Accounting service verifies only 1 accounting entry was ever created.
func TestWorkerCrashMidFlightReconcilesDuplicate(t *testing.T) {
	db := setupTestDB(t)

	type acctStore struct {
		sync.Mutex
		entries   map[string]string
		callCount int
	}
	store := &acctStore{entries: make(map[string]string)}

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ClientRef   string `json:"client_ref"`
			AmountPaise int64  `json:"amount_paise"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}

		store.Lock()
		defer store.Unlock()
		store.callCount++

		if existing, exists := store.entries[in.ClientRef]; exists {
			// Remote idempotency: duplicate client_ref returns 409 with original external_ref
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"external_ref": existing})
			return
		}

		ref := "acct-crash-sequence-1"
		store.entries[in.ClientRef] = ref
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"external_ref": ref})
	}))
	defer mockServer.Close()

	ctx := context.Background()
	clientRef := "receipt:crash-recovery-sim-001"
	var voucherID string
	err := db.QueryRow(ctx, `
		INSERT INTO vouchers(type, source_id, amount_paise, status, client_ref)
		VALUES('jobwork_receipt', gen_random_uuid(), 0, 'pending', $1)
		RETURNING id`, clientRef).Scan(&voucherID)
	if err != nil {
		t.Fatalf("insert voucher: %v", err)
	}

	// Step 2 & 3: Worker 1 claims voucher and calls accounting
	var v voucher
	err = db.QueryRow(ctx, `
		WITH claimed AS (
			SELECT id FROM vouchers WHERE id=$1::uuid FOR UPDATE
		)
		UPDATE vouchers v
		SET attempts = v.attempts + 1,
		    locked_until = now() + interval '15 seconds',
		    updated_at = now()
		FROM claimed WHERE v.id = claimed.id
		RETURNING v.id, v.client_ref, v.amount_paise, v.attempts`, voucherID).Scan(&v.ID, &v.ClientRef, &v.Amount, &v.Attempts)
	if err != nil {
		t.Fatalf("worker 1 claim: %v", err)
	}

	// Worker 1 makes HTTP call to accounting service
	reqBody, _ := json.Marshal(map[string]any{"client_ref": v.ClientRef, "amount_paise": v.Amount})
	resp, err := http.Post(mockServer.URL+"/api/vouchers", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("worker 1 http post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("worker 1 expected 201 Created from accounting, got %d", resp.StatusCode)
	}

	// Step 4: Worker 1 CRASHES before updating PostgreSQL!
	// At this point, accounting has the record ("acct-crash-sequence-1"), but PostgreSQL still has:
	// status='pending', attempts=1, locked_until in the future.
	var statusBeforeCrash string
	var attemptsBeforeCrash int
	_ = db.QueryRow(ctx, `SELECT status, attempts FROM vouchers WHERE id=$1::uuid`, voucherID).Scan(&statusBeforeCrash, &attemptsBeforeCrash)
	if statusBeforeCrash != "pending" || attemptsBeforeCrash != 1 {
		t.Fatalf("expected pending attempt 1 before recovery, got %s attempt %d", statusBeforeCrash, attemptsBeforeCrash)
	}

	// Step 5: Lease expires (simulating passage of 15 seconds)
	_, err = db.Exec(ctx, `UPDATE vouchers SET locked_until = now() - interval '1 second' WHERE id=$1::uuid`, voucherID)
	if err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	// Step 6: Worker 2 runs drain() to process the queue
	// Worker 2 should reclaim the voucher, resend the same client_ref, receive 409 from accounting,
	// and reconcile it into 'pushed'
	if err := drain(ctx, db, mockServer.URL); err != nil {
		t.Fatalf("worker 2 drain: %v", err)
	}

	// Step 7 & 8: Wait for Worker 2 to complete reconciliation
	var finalStatus, finalExtRef string
	var finalAttempts int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		err = db.QueryRow(ctx, `SELECT status, COALESCE(external_ref, ''), attempts FROM vouchers WHERE id=$1::uuid`, voucherID).Scan(&finalStatus, &finalExtRef, &finalAttempts)
		if err == nil && finalStatus == "pushed" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if finalStatus != "pushed" {
		t.Fatalf("expected voucher to be reconciled to 'pushed', got '%s'", finalStatus)
	}
	if finalExtRef != "acct-crash-sequence-1" {
		t.Fatalf("expected external_ref 'acct-crash-sequence-1', got '%s'", finalExtRef)
	}
	if finalAttempts != 2 {
		t.Fatalf("expected attempts 2 after retry, got %d", finalAttempts)
	}

	// Step 9: Verify remote accounting service state:
	store.Lock()
	defer store.Unlock()
	if store.callCount != 2 {
		t.Fatalf("expected exactly 2 HTTP calls (original + retry), got %d", store.callCount)
	}
	if len(store.entries) != 1 {
		t.Fatalf("expected exactly 1 accounting entry, got %d", len(store.entries))
	}
	if store.entries[clientRef] != "acct-crash-sequence-1" {
		t.Fatalf("unexpected entry in accounting: %v", store.entries)
	}
}
