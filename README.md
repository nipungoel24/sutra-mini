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

---

## 4. Database Setup & Migrations

If using local Docker PostgreSQL:
```powershell
docker compose up -d db
```

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
  "issue_id": "ea0a0710-e9df-4f46-ab40-142f33017af3",
  "qty": 10,
  "received_on": "2026-10-12T12:00:00Z"
}
```

**PowerShell Example:**
```powershell
$headers = @{ "Content-Type" = "application/json"; "Idempotency-Key" = "receipt-demo-001" }
$body = '{"issue_id":"ea0a0710-e9df-4f46-ab40-142f33017af3","qty":10,"received_on":"2026-10-12T12:00:00Z"}'
Invoke-RestMethod -Uri "http://localhost:8080/jobwork/receipts" -Method Post -Headers $headers -Body $body | ConvertTo-Json
```

### 2. Idempotency Replay
Replay the exact same request with the same `Idempotency-Key`:
- Returns HTTP 200 OK with the original receipt response.
- Creates zero additional receipts and zero additional vouchers.

### 3. Check Issue Balance
```powershell
Invoke-RestMethod -Uri "http://localhost:8080/issues/ea0a0710-e9df-4f46-ab40-142f33017af3"
```
**Response:**
```json
{
  "id": "ea0a0710-e9df-4f46-ab40-142f33017af3",
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

# Run all integration tests
go test -v ./...
```

### What the tests prove:
1. `TestConcurrentReceiptsCannotOvershoot`: Fires 10 concurrent receipt requests exceeding issued quantity. Proves accepted total equals issued quantity and overshoots return HTTP 409.
2. `TestIdempotencyReplayCreatesNothingTwice`: Proves replaying a request with the same key returns the original result and creates no duplicate database records.
3. `TestWorkerDuplicateReconciliation`: Proves the worker safely reconciles an HTTP 409 Conflict response by extracting the original `external_ref` and marking the voucher `pushed`.
4. `TestWorkerLeaseReclaimAfterCrash`: Simulates a worker crash leaving an expired lease (`locked_until < now()`). Proves a restarted worker safely reclaims and completes the push.
5. `TestWorkerExhaustionToFailed`: Proves that a voucher repeatedly failing remote calls is transitioned to `status='failed'` with `last_error` preserved after 6 attempts.
6. `TestTwoWorkersConcurrentProcessing`: Proves that two worker instances running simultaneously drain the queue without claiming the same voucher or interfering.

---

## 8. Frontend Production Build

To verify the Next.js production build:
```powershell
cd web
pnpm run build
```
Generates the optimized static `/sync` dashboard with zero TypeScript or lint errors.

---

## 9. Troubleshooting & FAQ

- **Database Connection Timeouts:** Ensure `sslmode=require` is present in the Neon connection string and network outbound port 5432 is accessible.
- **Port Conflicts:** The API runs on `:8080`, Mock Accounting on `:8081`, and Next.js on `:3000`. Configure `API_ADDR` or `ACCOUNTING_ADDR` in `.env` if needed.
- **`next` command not found:** Use `pnpm install` inside the `web/` directory.

---

## 10. Design Decisions & Limitations

1. **Voucher Amount (`amount_paise = 0`):** The case study brief did not specify item rates or job-work charges. To adhere strictly to requirements without inventing arbitrary business values, `amount_paise` defaults to `0`.
2. **Lease Duration:** The worker lease duration is set to 15 seconds. Since normal HTTP requests timeout after 2 seconds, 15 seconds provides ample buffer against network jitter while enabling fast crash recovery.
3. **Stretch `/ask` Goal:** Omitted as requested in the instructions to ensure 100% rigor and depth on core reliability and transactional guarantees.
