# Verification Record: Sutra Mini

**Audit Date:** 2026-10-12  
**Environment:** Windows 11 x64, PowerShell 5.1 / 7, Go 1.23.6, Node v22.20.0, pnpm 11.22.0  
**Repository Location:** `C:\Kaam_Dhanda\Projects\Shree-Radha-Studio`  
**Authoritative Specification:** `case_study_brief.pdf` (Round 2 Case Study)

---

## 1. Toolchain & Runtime Availability

| Tool | Status | Version | Path / Location | Verification Command & Result |
| :--- | :--- | :--- | :--- | :--- |
| **Git** | **PASS** | 2.x | `C:\Program Files\Git\cmd\git.exe` | `git status` -> OK |
| **Go** | **PASS** | `go1.23.6 windows/amd64` | `C:\Users\Nipun\AppData\Local\Programs\go\bin\go.exe` | `go version` -> Exit Code 0 |
| **Node.js** | **PASS** | `v22.20.0` | `C:\Program Files\nodejs\node.exe` | `node --version` -> Exit Code 0 |
| **npm** | **PASS** | `10.9.3` | `C:\Program Files\nodejs\npm.ps1` | `npm --version` -> Exit Code 0 |
| **pnpm** | **PASS** | `11.22.0` | `C:\Program Files\nodejs\pnpm.ps1` | `pnpm --version` -> Exit Code 0 |
| **Docker CLI** | **PASS** | `29.3.1` | `C:\Program Files\Docker\Docker\resources\bin\docker.exe` | `docker --version` -> Exit Code 0 |
| **Docker Desktop Daemon** | **STOPPED** | N/A | Service `com.docker.service` stopped | Not blocking; Neon PostgreSQL used directly |
| **PostgreSQL Client (psql)** | **PASS** | `18.4` | `C:\Program Files\PostgreSQL\18\bin\psql.exe` | `psql --version` -> Exit Code 0 |
| **Neon PostgreSQL Database** | **PASS** | PostgreSQL 17 serverless | Neon cloud (`aws us-east-2`) | `SELECT current_database();` -> `sutra`, `sutra_test` |

---

## 2. Git State & Secret Hygiene

- **Branch:** `master`
- **Working Tree:** Verified clean and tracked.
- **Secret Protection:** `.env` is ignored by `.gitignore` and not tracked by Git.
- **Git Status:** No credentials, API keys, or connection strings committed.
- **Remote:** Prepared for origin assignment upon user authorization.

---

## 3. Database Verification

- **Runtime Database:** `sutra` on Neon
  - Applied: `migrations/001_init.sql` (Tables: `skus`, `parties`, `jobwork_issues`, `jobwork_receipts`, `vouchers`, indexes)
  - Applied: `migrations/002_seed.sql` (5 SKUs with Unsplash images, 2 job-worker parties, 4 open issues)
  - Audit Query:
    ```sql
    SELECT count(*) FROM skus;           -- Returns 5
    SELECT count(*) FROM parties;        -- Returns 2
    SELECT count(*) FROM jobwork_issues; -- Returns 4
    ```
- **Test Database:** `sutra_test` on Neon
  - Applied: `migrations/001_init.sql`
  - Clean isolated database used by automated integration test suite (`TEST_DATABASE_URL`).

---

## 4. Test Suite Execution & Outcomes

| Test / Scenario | Target Package / Command | Result | Exit Code | Evidence / Observations |
| :--- | :--- | :--- | :--- | :--- |
| **Go Mod Tidy** | `go mod tidy` | **PASS** | 0 | All dependencies downloaded cleanly |
| **Go Code Compilation** | `go build ./...` | **PASS** | 0 | `cmd/api`, `cmd/mock-accounting`, `cmd/worker` compile with 0 warnings/errors |
| **Concurrent Overshoot Integration Test** | `go test -v -run TestConcurrentReceiptsCannotOvershoot ./cmd/api` | **PASS** | 0 | 10 concurrent requests fired against 5 available; exactly 5 accepted, 5 rejected with 409 Conflict. Total received = 5. |
| **Idempotency Replay Integration Test** | `go test -v -run TestIdempotencyReplayCreatesNothingTwice ./cmd/api` | **PASS** | 0 | Same key replayed twice; first returns 201 Created, second returns 200 OK with identical receipt ID. DB count = 1 receipt, 1 voucher. |
| **Worker 409 Duplicate Reconciliation** | `go test -v -run TestWorkerDuplicateReconciliation ./cmd/worker` | **PASS** | 0 | Mock returns 409 Conflict with `external_ref`. Worker validates payload and transitions voucher status to `pushed`. |
| **Worker Crash Recovery / Lease Reclaim** | `go test -v -run TestWorkerLeaseReclaimAfterCrash ./cmd/worker` | **PASS** | 0 | Voucher with expired lease (`locked_until < now()`) is claimed by worker run and pushed cleanly. |
| **Worker Retry Exhaustion to Failed** | `go test -v -run TestWorkerExhaustionToFailed ./cmd/worker` | **PASS** | 0 | Voucher at attempt 5 fails upstream -> attempt becomes 6, status transitions to `failed`, and `last_error` is preserved. |
| **Two Simultaneous Workers** | `go test -v -run TestTwoWorkersConcurrentProcessing ./cmd/worker` | **PASS** | 0 | 2 worker drain loops executed simultaneously over 6 vouchers; all 6 pushed with zero duplicate claims. |
| **Full Non-Cached Test Suite** | `go test -count=1 ./...` | **PASS** | 0 | All API and Worker tests passed against live PostgreSQL on Neon. |
| **Next.js Production Build** | `cd web && pnpm run build` | **PASS** | 0 | Turbopack production build succeeded; `/sync` static route generated cleanly with 0 TypeScript errors. |

---

## 5. End-to-End Live Verification Results

| Scenario | Execution Command / Action | Result | Verified Details |
| :--- | :--- | :--- | :--- |
| **A. Health & Startup** | `curl http://localhost:8080/healthz`<br>`curl http://localhost:8081/healthz` | **PASS** | Both API and Mock Accounting return `{"status":"ok"}`. |
| **B. Receipt Creation** | `POST /jobwork/receipts` with `Idempotency-Key: e2e-receipt-001`, `issue_id`, `qty: 10` | **PASS** | Returned 201 Created with `ID: 8d487f9d...`, `VoucherID: 8072b28a...`. Worker pushed voucher to accounting (`acct-20261012083312...`). |
| **C. Idempotency Replay** | Replayed same request with same key `e2e-receipt-001` | **PASS** | Returned original receipt ID with updated status `pushed`. Direct database query confirmed exactly 1 receipt and 1 voucher row. |
| **D. Ten Concurrent Receipts** | 10 concurrent requests fired for `qty: 10` against issue `SR-104` (`qty: 40`) | **PASS** | Exactly 4 requests returned 201 Created, 6 rejected with 409 Conflict (`receipt would exceed issued quantity`). Total received in DB strictly equals 40; outstanding = 0. |
| **E. Successful Sync** | Background worker auto-claim loop | **PASS** | Worker atomically claims pending voucher with `FOR UPDATE SKIP LOCKED` and lease; updates `status='pushed'` with `external_ref`. |
| **F. Transient Failure & Backoff** | Mock accounting 20% simulated 500 failure | **PASS** | Worker logs simulated failure, increments attempts, applies exponential backoff (`delay = 2^attempts seconds`), and successfully retries on subsequent run. |
| **G. Manual Retry on Failed Voucher** | `POST /vouchers/{id}/retry` on failed voucher | **PASS** | API requeues voucher (`status='pending'`, resets `locked_until=NULL`); worker immediately claims it and completes synchronization. |
| **H. Slow Accounting Request** | Mock accounting 10% simulated 5-second hang | **PASS** | Worker outbound HTTP timeout is 2 seconds; slow request times out and does not block other vouchers being processed concurrently in goroutines. |
| **I. Worker Crash Recovery** | Crash during in-flight HTTP call | **PASS** | 15-second `locked_until` lease expires; surviving/restarted worker re-claims voucher with same stable `client_ref`; mock returns 409 with original `external_ref`; worker reconciles and marks pushed. |
| **J. Two Simultaneous Workers** | Two independent worker processes running simultaneously | **PASS** | Processed queue of live vouchers concurrently without duplicate deliveries or database conflicts. |
| **K. Database State Direct Inspection** | `psql` queries on `vouchers` and `jobwork_receipts` | **PASS** | Quantities, paise amounts, client refs, external refs, and statuses match invariants. |
| **L. Frontend Dashboard (/sync)** | Rendered `http://localhost:3000/sync` | **PASS** | Responsive layout, 3 status columns (Pending, Pushed, Failed), attempt counters, live auto-refresh, and accessible retry action verified. |

---

## 6. Submission Deliverables Summary

- [x] Go API compiles and runs (`cmd/api`)
- [x] Database migrations apply cleanly (`migrations/001_init.sql`, `migrations/002_seed.sql`)
- [x] Seed data verified (5 SKUs, 2 parties, 4 open issues)
- [x] Single database transaction for receipt + voucher
- [x] Concurrent overshoot prevented via PostgreSQL row locking
- [x] Idempotency enforced via PostgreSQL unique constraint
- [x] Accounting mock implements 20% 500, 10% 5s delay, and 409 duplicate handling
- [x] Worker handles leases, retries, backoff, and duplicate reconciliation
- [x] Worker crash recovery verified
- [x] Two concurrent workers verified
- [x] Explicit outbound HTTP timeouts enforced
- [x] Mandatory integration tests pass
- [x] Next.js production build passes (`cd web && pnpm run build`)
- [x] `/sync` dashboard renders against real backend
- [x] README is fully reproducible for Windows & Linux
- [x] AGENT_NOTES.md is candid and comprehensive
- [x] .env.example is complete and clean of secrets
- [x] No secrets committed
- [x] Requirements audit current (`docs/REQUIREMENTS_AUDIT.md`)
- [x] Demo script ready (`docs/DEMO_SCRIPT.md`)
- [ ] Public GitHub repository (Awaiting user authorization to push)
- [ ] 3-minute screen recording (Script prepared in `docs/DEMO_SCRIPT.md`)
