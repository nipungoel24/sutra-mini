# Requirements-to-Code Matrix: Sutra Mini

**Project:** Sutra Mini  
**Organization:** Shree Radha Studio  
**Audit Date:** 2026-10-12  
**Authoritative Source:** `case_study_brief.pdf` (Round 2 Case Study)

---

## 1. Database & Schema Requirements

| Requirement | Source File / Artifact | Test / Verification | Status | Evidence | Missing / Needed Work |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **SKUs Table** (`id, code, name, image_url`) | `migrations/001_init.sql` | `psql \dt skus`, Integration tests | **PASS** | Table created on Neon; verified with `psql` | None |
| **Parties Table** (`id, name, phone`) | `migrations/001_init.sql` | `psql \dt parties`, Integration tests | **PASS** | Table created on Neon; verified with `psql` | None |
| **Job-work Issues Table** (`id, party_id, sku_id, qty, issued_on`) | `migrations/001_init.sql` | Foreign key references verified, Integration tests | **PASS** | Table created on Neon; verified with `psql` | None |
| **Job-work Receipts Table** (`id, issue_id, qty, received_on, idempotency_key`) | `migrations/001_init.sql` | `TestConcurrentReceiptsCannotOvershoot`, `TestIdempotencyReplayCreatesNothingTwice` | **PASS** | `idempotency_key UNIQUE`, `qty > 0 CHECK`, FK to `jobwork_issues` | None |
| **Accounting Vouchers Table** (`id, type, source_id, amount_paise, status, attempts, last_error, client_ref, external_ref, created_at, updated_at`) | `migrations/001_init.sql` | Worker lifecycle tests, API list vouchers | **PASS** | `UNIQUE(type, source_id)`, `client_ref UNIQUE`, `status CHECK`, `next_attempt_at`, `locked_until` | None |
| **5 Seeded SKUs with image URLs** | `migrations/002_seed.sql` | `SELECT count(*) FROM skus` | **PASS** | 5 SKUs seeded (SR-101 to SR-105) with valid Unsplash images | None |
| **2 Seeded Job-workers** | `migrations/002_seed.sql` | `SELECT count(*) FROM parties` | **PASS** | 2 parties seeded: Meera Embroidery Works, Kavya Stitching Unit | None |
| **4 Seeded Open Issues** | `migrations/002_seed.sql` | `SELECT count(*) FROM jobwork_issues` | **PASS** | 4 issues seeded: SR-101 (100), SR-102 (80), SR-103 (60), SR-104 (40) | None |
| **PostgreSQL migrations compatible with Neon** | `migrations/001_init.sql`, `migrations/002_seed.sql` | Executed against Neon instance | **PASS** | Clean execution with psql on Neon; extensions and tables verified | None |

---

## 2. API Endpoints

| Requirement | Source File / Endpoint | Test / Verification | Status | Evidence | Missing / Needed Work |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **POST /jobwork/receipts**<br>- Validate `Idempotency-Key` header<br>- Validate `issue_id, qty > 0, received_on`<br>- Serialize receipt writers via issue row lock (`FOR UPDATE`)<br>- Replay same key returns original result & creates nothing twice<br>- Atomic receipt + voucher creation in single tx<br>- Prevent receipt overshoot | `cmd/api/main.go` (`createReceipt`) | `TestConcurrentReceiptsCannotOvershoot`, `TestIdempotencyReplayCreatesNothingTwice` | **PASS** | Row lock with `SELECT qty FROM jobwork_issues WHERE id=$1::uuid FOR UPDATE`, atomic transaction, unique idempotency key | None |
| **GET /issues/{id}**<br>Returns `{id, issued, received, outstanding}` | `cmd/api/main.go` (`getIssue`) | Integration test & HTTP endpoint curl | **PASS** | `LEFT JOIN jobwork_receipts` grouped by `issue.id` | None |
| **GET /vouchers?status=**<br>Filters vouchers by status | `cmd/api/main.go` (`listVouchers`) | Integration test & Frontend sync query | **PASS** | Filtered SQL query returning attempts, errors, refs | None |
| **POST /vouchers/{id}/retry**<br>Requeues failed voucher | `cmd/api/main.go` (`retryVoucher`) | Integration test & UI retry click | **PASS** | `UPDATE vouchers SET status='pending', next_attempt_at=now(), locked_until=NULL WHERE id=$1 AND status='failed'` | None |

---

## 3. Accounting Mock Service

| Requirement | Source File / Endpoint | Test / Verification | Status | Evidence | Missing / Needed Work |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **POST /api/vouchers** | `cmd/mock-accounting/main.go` | Unit & Integration tests | **PASS** | In-memory store with `sync.Mutex` | None |
| **20% simulated HTTP 500 responses** | `cmd/mock-accounting/main.go` | Mock test suite | **PASS** | `n < 20` returns HTTP 500 with JSON error message | None |
| **10% simulated 5-second delay followed by 201** | `cmd/mock-accounting/main.go` | Mock test suite & worker timeout test | **PASS** | `n < 30` sleeps 5 seconds then returns 201 Created | None |
| **Duplicate `client_ref` returns HTTP 409 with original `external_ref`** | `cmd/mock-accounting/main.go` | Duplicate reconciliation test | **PASS** | Checks `s.refs[x.ClientRef]` and returns HTTP 409 with `{external_ref}` | None |

---

## 4. Background Voucher Sync Worker

| Requirement | Source File | Test / Verification | Status | Evidence | Missing / Needed Work |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Atomic Claiming with Leases** | `cmd/worker/main.go` (`drain`) | Worker concurrency test | **PASS** | `WITH claimed AS (SELECT id FROM vouchers WHERE status='pending' AND next_attempt_at<=now() AND (locked_until IS NULL OR locked_until<now()) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE ... locked_until=now()+interval '15 seconds'` | None |
| **Retries & Exponential Backoff** | `cmd/worker/main.go` (`push`) | Retry state transition test | **PASS** | `delay := time.Duration(1 << min(v.Attempts, 6)) * time.Second` | None |
| **Mark Persistent Failures Failed** | `cmd/worker/main.go` (`push`) | Exhaustion test | **PASS** | When `v.Attempts >= 6`, set `status='failed'` and store `last_error` | None |
| **Record Failure Reason** | `cmd/worker/main.go` (`push`) | UI sync card display | **PASS** | `last_error` stores error text or HTTP status | None |
| **Manual Retry Support** | `cmd/api/main.go` (`retryVoucher`) | End-to-end retry test | **PASS** | Clears `locked_until`, resets `next_attempt_at=now()`, resets `status='pending'` | None |
| **Safe Concurrent Worker Instances (2 instances)** | `cmd/worker/main.go` | Concurrency integration test | **PASS** | `FOR UPDATE SKIP LOCKED` guarantees no two workers claim same voucher | None |
| **Explicit Outbound HTTP Timeouts** | `cmd/worker/main.go` (`push`) | Timeout test | **PASS** | `context.WithTimeout(ctx, 2*time.Second)` | None |
| **Slow Voucher Does Not Block Others** | `cmd/worker/main.go` (`drain`) | Async dispatch test | **PASS** | `go push(...)` in goroutine, worker loop continues | None |
| **Crash Recovery During HTTP Call** | `cmd/worker/main.go` | Crash simulation test | **PASS** | `locked_until` expires after 15s; on retry, same `client_ref` sent | None |
| **Reconcile Duplicate (409) via `client_ref`** | `cmd/worker/main.go` (`push`) | `TestWorkerDuplicateReconciliation` | **PASS** | Validates `resp.ExternalRef != ""` on HTTP 409 before marking `status='pushed'` | None |

---

## 5. Correctness Guarantees

| Requirement | Implementation Detail | Test / Verification | Status | Evidence |
| :--- | :--- | :--- | :--- | :--- |
| **Single Database Transaction** | Receipt and voucher inserted in same `tx` | `cmd/api/main.go` | **PASS** | Both inserts executed within `s.db.BeginTx` and committed atomically |
| **No Concurrent Overshoot** | Issue locked with `FOR UPDATE`; `SUM(qty)` checked | `cmd/api/receipt_integration_test.go` | **PASS** | `TestConcurrentReceiptsCannotOvershoot` validates concurrent requests cannot exceed issue qty |
| **Idempotency Replay Safety** | Check before and after lock; unique constraint on `idempotency_key` | `cmd/api/receipt_integration_test.go` | **PASS** | `TestIdempotencyReplayCreatesNothingTwice` confirms replay returns original receipt without duplicate |
| **Integer Paise (No Floats)** | `amount_paise bigint` in schema and Go `int64` | `migrations/001_init.sql`, Go models | **PASS** | Zero floating-point types used for monetary values |
| **Context Propagation** | `r.Context()` passed to database and HTTP requests | `cmd/api/main.go`, `cmd/worker/main.go` | **PASS** | Timeouts and cancellations propagate cleanly |

---

## 6. Frontend Dashboard (/sync)

| Requirement | Source File | Test / Verification | Status | Evidence |
| :--- | :--- | :--- | :--- | :--- |
| **Plain, Accessible Layout** | `web/app/sync/page.tsx`, `web/app/styles.css` | Production build, UI inspection | **PASS** | High contrast, semantic HTML, visible focus states, responsive CSS |
| **Grouped by Pending / Pushed / Failed** | `web/app/sync/page.tsx` | UI inspection | **PASS** | 3 columns with status counts |
| **Attempt Counts & Failure Reasons** | `web/app/sync/page.tsx` | UI inspection | **PASS** | Displayed in `<dl>` and `<p className="reason">` |
| **Retry Action on Failed Vouchers** | `web/app/sync/page.tsx` | End-to-end browser test | **PASS** | Calls `POST /vouchers/{id}/retry` and refreshes list |
| **Loading / Error / Empty States** | `web/app/sync/page.tsx` | UI inspection | **PASS** | Role alert error banner, "No ... vouchers" empty state |
| **Next.js Production Build** | `web/` | `cd web && pnpm run build` | **PASS** | Turbopack build succeeds in 2.5s with zero errors |

---

## 7. Deliverables & Documentation

| Deliverable | Location | Status | Current State |
| :--- | :--- | :--- | :--- |
| **README.md** | Root | **PASS** | Complete PowerShell and clean-machine instructions, testing, architecture |
| **AGENT_NOTES.md** | Root | **PASS** | Candid notes on tools, issues caught, fixes, limitations |
| **docs/VERIFICATION.md** | `docs/VERIFICATION.md` | **PASS** | Live audit record, command output, and test execution evidence |
| **docs/DEMO_SCRIPT.md** | `docs/DEMO_SCRIPT.md` | **PASS** | Comprehensive 3-minute video recording script |
| **.env.example** | Root | **PASS** | Verified clean, contains no secrets |
| **Public GitHub Repository** | Remote | **READY** | Prepared for push once user provides remote authorization |
| **3-minute Screen Recording** | Demo | **READY** | Script and application state ready for user recording |
