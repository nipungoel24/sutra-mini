# Sutra Mini: Reliable Job-Work Accounting Push

**Organization:** Shree Radha Studio  
**Role:** Full Stack and Applied-AI Engineer Case Study (Round 2)  
**Stack:** Go 1.23+ (Backend API & Worker), PostgreSQL / Neon (Database), Next.js 16 / React 19 (Frontend Dashboard)

---

## 1. Overview & Architecture

In ethnicwear manufacturing, raw fabric is issued to embroidery and stitching job-workers and returned in partial batches over days. Every job-work receipt produces an accounting consequence that must reach the accounting software. However, the external accounting API is slow, hangs intermittently, and experiences transient 500 failures without warning.

**Sutra Mini** is the reliability layer designed to make this pipeline bulletproof:
1. **Atomic Transactional Invariant:** When a receipt is recorded, its accounting voucher is inserted in the exact same database transaction.
2. **PostgreSQL Row-Locking Concurrency Control:** Receipt writers for an issue are serialized with `SELECT qty FROM jobwork_issues WHERE id = $1::uuid FOR UPDATE`. Total received quantity can never overshoot issued quantity, even under concurrent bursts.
3. **Database-Enforced Idempotency:** The `Idempotency-Key` header is checked in PostgreSQL before and after acquiring the row lock. A replay returns the original receipt record and creates nothing twice.
4. **Crash-Safe Worker Synchronization:** The background worker leases vouchers atomically with `FOR UPDATE SKIP LOCKED` and a 15-second lease (`locked_until`). Outbound HTTP calls have an explicit 2-second timeout. If a worker crashes mid-flight, the lease expires and allows surviving or restarted workers to reclaim the voucher.
5. **Idempotent Remote Reconciliation:** Vouchers carry a stable, durable `client_ref` (`receipt:<receipt_id>`). When retrying after a crash or timeout, the remote accounting service detects the duplicate and returns `HTTP 409 Conflict` with the original `external_ref`. The worker reconciles this response and marks the voucher as `pushed`.
6. **Multi-Worker Concurrency:** Two or more independent worker instances can run simultaneously without claiming the same voucher or duplicating accounting entries.
7. **Operational Dashboard:** A plain, semantic, high-contrast Next.js `/sync` dashboard displays vouchers grouped by state (Pending, Pushed, Failed), attempt counts, failure reasons, and a manual Retry button.

---

## 2. Prerequisites

- **Go:** 1.23 or newer (`go version`)
- **Node.js:** 20 or newer (`node --version`)
- **Package Manager:** `pnpm` (recommended, `pnpm -v`) or `npm`
- **PostgreSQL Client:** `psql` (or Docker for local containerized Postgres)
- **Database:** A local PostgreSQL instance or a free-tier [Neon](https://neon.tech) PostgreSQL connection string

---

## 3. Environment Configuration

Copy `.env.example` to `.env`:

### Windows PowerShell:
```powershell
Copy-Item .env.example .env
```

### macOS / Linux:
```bash
cp .env.example .env
```

Configure the following variables in `.env`:
```env
DATABASE_URL=postgres://user:password@ep-xyz.us-east-2.aws.neon.tech/sutra?sslmode=require
TEST_DATABASE_URL=postgres://user:password@ep-xyz.us-east-2.aws.neon.tech/sutra_test?sslmode=require
API_ADDR=:8080
ACCOUNTING_URL=http://localhost:8081
NEXT_PUBLIC_API_BASE=http://localhost:8080
```

> **Security Note:** `.env` is ignored in `.gitignore`. Never commit connection strings or credentials.

Optional variables:

| Variable | Used by | Default | Purpose |
| :--- | :--- | :--- | :--- |
| `ACCOUNTING_ADDR` | mock-accounting | `:8081` | Listen address of the mock |
| `MOCK_MODE` | mock-accounting | random | `deterministic-delay` (every new voucher hangs 5 s), `deterministic-fail` (every new voucher gets 500), `deterministic-success`. Unset = 20% 500 / 10% 5 s hang. |
| `CORS_ORIGIN` | api | `*` | Pin the allowed dashboard origin, e.g. `http://localhost:3000` |

`TEST_DATABASE_URL` **must point at a separate database** from `DATABASE_URL`: the integration tests truncate every table. The tests refuse to run if the two values are identical.

---

## 4. Database Setup & Migrations

If using local Docker PostgreSQL (matches the defaults in `.env.example`):
```powershell
docker compose up -d db
# docker-compose only creates the "sutra" database; create the test database once:
docker compose exec db psql -U postgres -c "CREATE DATABASE sutra_test"
```

If using Neon, create two databases (e.g. `sutra` and `sutra_test`) in the Neon console and paste both connection strings into `.env`.

Apply migrations and seed data using `psql`:

### Windows PowerShell:
```powershell
# Load DATABASE_URL from .env
$env:DATABASE_URL = (Get-Content .env | Where-Object { $_ -match '^DATABASE_URL=(.*)' } | ForEach-Object { $matches[1] })

# Apply schema migrations
& psql "$env:DATABASE_URL" -f migrations/001_init.sql

# Apply seed data
& psql "$env:DATABASE_URL" -f migrations/002_seed.sql
```

### macOS / Linux:
```bash
export DATABASE_URL=$(grep '^DATABASE_URL=' .env | cut -d '=' -f2-)
psql "$DATABASE_URL" -f migrations/001_init.sql
psql "$DATABASE_URL" -f migrations/002_seed.sql
```

### Seed Inventory Summary
- **5 SKUs:** `SR-101` (Embroidered Kurta), `SR-102` (Chanderi Dupatta), `SR-103` (Handblock Saree), `SR-104` (Festive Anarkali), `SR-105` (Silk Co-ord Set) with image URLs.
- **2 Job-Workers:** `Meera Embroidery Works`, `Kavya Stitching Unit`.
- **4 Open Issues:**
  - Issue 1: Meera Embroidery Works, SR-101 (Qty: 100)
  - Issue 2: Kavya Stitching Unit, SR-102 (Qty: 80)
  - Issue 3: Meera Embroidery Works, SR-103 (Qty: 60)
  - Issue 4: Kavya Stitching Unit, SR-104 (Qty: 40)

Issue ids are generated UUIDs, so they differ on every machine. List them before calling the API:
```powershell
& psql "$env:DATABASE_URL" -c "SELECT i.id, s.code, p.name, i.qty FROM jobwork_issues i JOIN skus s ON s.id=i.sku_id JOIN parties p ON p.id=i.party_id ORDER BY s.code"
```
The examples below use `$issue` / `$ISSUE` for one of these ids.

---

## 5. Running the Application Locally

Run each service in a separate terminal:

### Terminal 1: Mock Accounting Service (Unreliable Remote System)
```powershell
go run ./cmd/mock-accounting
```
*Listens on `:8081`. Simulates 20% HTTP 500 errors, 10% 5-second delays followed by 201 Created, and returns HTTP 409 Conflict with the existing `external_ref` for duplicate `client_ref` values.*

### Terminal 2: Sutra Go API
```powershell
# In PowerShell:
$lines = Get-Content .env
foreach ($l in $lines) { if ($l -match '^([A-Z_]+)=(.*)') { Set-Item -Path "env:$($matches[1])" -Value $matches[2] } }
go run ./cmd/api
```
*Listens on `:8080`. Serves `/jobwork/receipts`, `/issues/{id}`, `/vouchers`, and `/vouchers/{id}/retry`.*

### Terminal 3: Background Voucher Sync Worker
```powershell
# In PowerShell:
$lines = Get-Content .env
foreach ($l in $lines) { if ($l -match '^([A-Z_]+)=(.*)') { Set-Item -Path "env:$($matches[1])" -Value $matches[2] } }
go run ./cmd/worker
```
*Autonomously leases eligible pending vouchers with `FOR UPDATE SKIP LOCKED`, pushes them to accounting with explicit 2s timeout, handles exponential backoff, reconciles duplicates, and updates status.*

### Terminal 4: Next.js `/sync` Operational Dashboard
```powershell
cd web
pnpm install
pnpm run dev
```
*Open [http://localhost:3000/sync](http://localhost:3000/sync) in your browser.*

---

## 6. API Reference & Usage Examples

### 1. Record a Job-Work Receipt
```http
POST /jobwork/receipts HTTP/1.1
Content-Type: application/json
Idempotency-Key: receipt-demo-001

{
  "issue_id": "<issue id from the query above>",
  "qty": 10,
  "received_on": "2026-10-12T12:00:00Z"
}
```

`received_on` must be an RFC 3339 timestamp (`2026-10-12T12:00:00Z`); it is stored as a date.

**PowerShell Example:**
```powershell
$issue = "<issue id from the query above>"
$headers = @{ "Idempotency-Key" = "receipt-demo-001" }
$body = "{""issue_id"":""$issue"",""qty"":10,""received_on"":""2026-10-12T12:00:00Z""}"
Invoke-RestMethod -Uri "http://localhost:8080/jobwork/receipts" -Method Post -ContentType "application/json" -Headers $headers -Body $body | ConvertTo-Json
```

**Response (201 Created):**
```json
{
  "id": "<receipt id>",
  "issue_id": "<issue id>",
  "voucher_id": "<voucher id>",
  "voucher_status": "pending",
  "qty": 10,
  "received_on": "2026-10-12T00:00:00Z"
}
```

| Status | Meaning |
| :--- | :--- |
| 201 | Receipt and pending voucher created in one transaction |
| 200 | Replay of an existing `Idempotency-Key`: same receipt and voucher ids, nothing created (`voucher_status` shows the current sync state) |
| 400 | Missing/blank `Idempotency-Key`, malformed JSON, non-UUID `issue_id`, `qty <= 0`, missing `received_on` |
| 404 | Issue does not exist |
| 409 | Receipt would exceed the issued quantity (or the same key raced in concurrently for a different issue) |

### 2. Idempotency Replay
Replay the exact same request with the same `Idempotency-Key`:
- Returns HTTP 200 OK with the original receipt and voucher ids.
- Creates zero additional receipts and zero additional vouchers.
- The key identifies the request: a replay with the same key but a different body still returns the original receipt (the body is not compared).

### 3. Check Issue Balance
```powershell
Invoke-RestMethod -Uri "http://localhost:8080/issues/$issue"
```
**Response:**
```json
{
  "id": "<issue id>",
  "issued": 100,
  "received": 10,
  "outstanding": 90
}
```

### 4. List Vouchers
```powershell
Invoke-RestMethod -Uri "http://localhost:8080/vouchers?status=failed"
```

### 5. Requeue a Failed Voucher
```powershell
Invoke-RestMethod -Uri "http://localhost:8080/vouchers/<voucher_id>/retry" -Method Post
```
Returns 200 when a `failed` voucher is requeued, 409 if the voucher is not `failed`, 404 if it does not exist. The retry grants one more attempt (the attempt counter is kept as history, not reset), so if accounting is still failing the voucher returns to `failed` after that attempt.

---

## 6a. Reproducing the Evaluation Scenarios

These were run end to end against the real binaries on 2026-10-14 (see `docs/CLAUDE_CODE_AUDIT.md`).

**Replay the same key:** send the request from section 6.1 twice. The first returns 201, the second returns 200 with identical `id` and `voucher_id`.

**Ten concurrent receipts** (bash / Git Bash; SR-104 has 40 issued, so with `qty=10` exactly 4 succeed):
```bash
for i in $(seq 1 10); do curl -s -o /dev/null -w "%{http_code}\n" -X POST localhost:8080/jobwork/receipts -H "Content-Type: application/json" -H "Idempotency-Key: conc-$i" -d "{\"issue_id\":\"$ISSUE\",\"qty\":10,\"received_on\":\"2026-10-14T10:00:00Z\"}" & done; wait
```
Expected: four `201`, six `409`; `GET /issues/$ISSUE` shows `received: 40, outstanding: 0`.

**Kill the worker during the 5-second hang and restart it:**
1. Start the mock with `MOCK_MODE=deterministic-delay` so every new voucher hangs (PowerShell: `$env:MOCK_MODE="deterministic-delay"; go run ./cmd/mock-accounting`).
2. Create a receipt, start the worker, and kill it (Ctrl+C or close the terminal) while the mock logs `Simulating 5s delay`. The mock has already recorded the entry; Postgres still shows the voucher `pending`.
3. Restart the worker. The old lease (`locked_until`, 15 s) must expire first, so the voucher stays `pending` for up to 15 s. The worker then re-sends the same `client_ref`, the mock answers `409` with the original `external_ref`, and the voucher becomes `pushed`. The mock log shows exactly one accepted entry for that `client_ref`.

Note: the worker's HTTP timeout is 2 s, shorter than the 5 s hang, so even without a kill a hung call times out, is retried with backoff, and is settled through the 409 path. The hang never blocks other vouchers because each push runs in its own goroutine after the claim transaction has committed.

**Two workers at once:** start `go run ./cmd/worker` in two terminals and create several receipts. Each voucher is claimed by one worker (`FOR UPDATE SKIP LOCKED` plus the lease); the mock log shows one accepted entry per `client_ref`.

**A failed voucher and a manual retry:** run the mock with `MOCK_MODE=deterministic-fail`, create a receipt, and wait about 70 s (backoff 2+4+8+16+32 s across 6 attempts). The voucher appears under **Failed** on `/sync` with the reason. Restart the mock without `MOCK_MODE` and click **Retry**: the voucher goes back to Pending and then Pushed (80% chance per attempt in random mode; use `MOCK_MODE=deterministic-success` for a guaranteed success on camera).

The mock keeps its `client_ref` memory in process memory. Restarting the **mock** forgets earlier entries; restart only the worker when demonstrating duplicate reconciliation.

---

## 7. Automated Integration Testing

The test suite requires a real PostgreSQL database (e.g. `sutra_test` on Neon) to verify real concurrency guarantees. It never mocks the database.

Apply `migrations/001_init.sql` to your test database once:
```powershell
& psql "$env:TEST_DATABASE_URL" -f migrations/001_init.sql
```

Run the complete test suite:
```powershell
# Set TEST_DATABASE_URL
$env:TEST_DATABASE_URL = (Get-Content .env | Where-Object { $_ -match '^TEST_DATABASE_URL=(.*)' } | ForEach-Object { $matches[1] })

# Run all integration tests. -p 1 is required: cmd/api and cmd/worker both truncate
# tables in the same test database, so their packages must not run in parallel.
go test -v -p 1 -count=1 ./...
```

Without `TEST_DATABASE_URL` the tests are skipped (reported as `ok`/`SKIP`), not passed; check the `-v` output for `--- PASS`.

### What the tests prove:
1. `TestConcurrentReceiptsCannotOvershoot`: Fires 10 concurrent receipt requests exceeding issued quantity. Proves accepted total equals issued quantity and overshoots return HTTP 409.
2. `TestIdempotencyReplayCreatesNothingTwice`: Proves replaying a request with the same key returns the original result and creates no duplicate database records.
3. `TestWorkerDuplicateReconciliation`: Proves the worker safely reconciles an HTTP 409 Conflict response by extracting the original `external_ref` and marking the voucher `pushed`.
4. `TestWorkerLeaseReclaimAfterCrash`: Simulates a worker crash leaving an expired lease (`locked_until < now()`). Proves a restarted worker safely reclaims and completes the push.
5. `TestWorkerExhaustionToFailed`: Proves that a voucher repeatedly failing remote calls is transitioned to `status='failed'` with `last_error` preserved after 6 attempts.
6. `TestTwoWorkersConcurrentProcessing`: Runs two `drain` loops simultaneously over 6 vouchers and asserts every `client_ref` reached accounting exactly once and every voucher was claimed exactly once.
7. `TestWorkerCrashMidFlightReconcilesDuplicate`: Deterministically simulates Worker 1 dying after the remote accounting service creates a record, lease expiring, Worker 2 (the production `drain`/`push` code) retrying with identical `client_ref`, receiving 409 Conflict, and reconciling the voucher to `pushed` with exactly one remote entry.
8. `TestStaleWorkerResultIsDiscarded`: A worker whose lease expired and whose voucher was reclaimed cannot overwrite the newer state with its late result (result writes are fenced on `status='pending' AND attempts=<its claim>`).
9. `TestReceiptRejectsInvalidInputWithoutLeakingSQL`: Malformed ids, bad JSON, `qty=0` and blank keys return 4xx without database error text and create nothing.

Tests 1, 2, 6 and 8 were checked by mutation: removing the issue row lock, the idempotency lookup, the lease condition, or the result-write fence makes the corresponding test fail.

---

## 8. Frontend Production Build

To verify the Next.js production build:
```powershell
cd web
pnpm run build
```
Generates the optimized static `/sync` dashboard and type-checks it (no separate linter is configured). The dashboard reads `NEXT_PUBLIC_API_BASE` (default `http://localhost:8080`); Next.js loads env files from `web/`, not the repo root, so set it in `web/.env.local` only if the API runs elsewhere.

---

## 9. Troubleshooting & FAQ

- **Database Connection Timeouts:** Ensure `sslmode=require` is present in the Neon connection string and network outbound port 5432 is accessible.
- **Port Conflicts:** The API runs on `:8080`, Mock Accounting on `:8081`, and Next.js on `:3000`. Configure `API_ADDR` or `ACCOUNTING_ADDR` in `.env` if needed.
- **`next` command not found:** Use `pnpm install` inside the `web/` directory.

---

## 10. Design Decisions & Limitations

1. **Voucher Amount (`amount_paise = 0`):** The case study brief did not specify item rates or job-work charges. To adhere strictly to requirements without inventing arbitrary business values, `amount_paise` defaults to `0`.
2. **Lease Duration:** The worker lease duration is set to 15 seconds. Since HTTP requests time out after 2 seconds, 15 seconds leaves a wide buffer, and crash recovery takes at most ~15 s. If a worker is somehow slower than its lease (e.g. paused process), its late result write is fenced out by the `attempts` check instead of overwriting the reclaiming worker's state.
3. **What "never posted twice" means here:** a request can be *sent* more than once (after a crash or timeout). Duplicates are prevented by the stable `client_ref` (`receipt:<receipt_id>`) that the accounting side treats as an idempotency key, and the worker settles the 409 response with the original `external_ref`. A 409 without an `external_ref` is treated as a failure, not success. This relies on the accounting system honouring `client_ref`; the mock keeps that memory in process, so restarting the mock loses it.
4. **Retry budget:** 6 attempts with exponential backoff (2, 4, 8, 16, 32 s), then `failed` with `last_error`. A timeout on the final attempt marks the voucher `failed` even though accounting may have accepted it; a manual retry then settles it through the 409 path without creating a duplicate.
5. **Shutdown:** on Ctrl+C the worker stops claiming and exits; in-flight pushes are abandoned rather than awaited. This is safe (the lease expires and the next worker reconciles through 409) but not a drain-style graceful shutdown.
6. **Local demo security:** there is no authentication on the API and CORS defaults to `*`. This is a local case-study service, not production-hardened.
7. **Stretch `/ask` Goal:** Omitted, so the time went into the core reliability and transactional guarantees.
