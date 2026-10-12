# Sutra Mini: 3-Minute Demonstration Script

**Role:** Full Stack and Applied-AI Engineer Case Study (Round 2)  
**Organization:** Shree Radha Studio  
**Target Duration:** Exactly 3 minutes (180 seconds)  
**Target URL:** `http://localhost:3000/sync`  
**API Endpoint:** `http://localhost:8080`

---

## Recording Setup & Preparation Checklist

Before hitting record:
1. Ensure all services are active:
   - Mock Accounting: `go run ./cmd/mock-accounting` (`:8081`)
   - Go API: `go run ./cmd/api` (`:8080`)
   - Background Worker: `go run ./cmd/worker`
   - Frontend Dashboard: `cd web && pnpm run dev` (`:3000/sync`)
2. Open Chrome/browser to `http://localhost:3000/sync` on the left half of the screen.
3. Open a clean PowerShell / terminal window on the right half of the screen.
4. Have the pre-tested cURL / PowerShell commands copied and ready.

---

## Minute-by-Minute Demo Flow

### 0:00 – 0:30 (30s) — Problem Context & Dashboard Overview
- **Visual:** Show browser on `http://localhost:3000/sync`.
- **Narration:**
  > "Hi, I'm presenting Sutra Mini for Shree Radha Studio. In our ethnicwear supply chain, fabric goes out to embroidery and stitching job-workers and returns in partial batches over days. Every receipt creates an accounting voucher that must reach our remote accounting system. But that accounting API is slow, unpredictable, and experiences transient 500 errors and delays. Sutra is the reliability layer that guarantees correctness: no inventory overshoot, strictly safe idempotency, and crash-safe voucher synchronization."
- **Visual Action:** Point out the three columns on `/sync`: Pending, Pushed, and Failed, showing attempt counts, live status badges, and durable client references (`receipt:<id>`).

---

### 0:30 – 1:15 (45s) — Receipt Creation & Database Idempotency Replay
- **Narration:**
  > "First, let's create a valid job-work receipt for Issue SR-101. We send a `POST /jobwork/receipts` with an `Idempotency-Key` header."
- **Terminal Command:**
  ```powershell
  $body = '{"issue_id":"ea0a0710-e9df-4f46-ab40-142f33017af3","qty":10,"received_on":"2026-10-12T12:00:00Z"}'
  Invoke-RestMethod -Uri "http://localhost:8080/jobwork/receipts" -Method Post -Headers @{"Content-Type"="application/json"; "Idempotency-Key"="demo-receipt-001"} -Body $body | ConvertTo-Json
  ```
- **Visual:** HTTP 201 Created returns receipt ID and pending voucher ID. Within seconds, the dashboard auto-refreshes, showing the voucher move to `Pushed` with an `external_ref`.
- **Narration:**
  > "Now, suppose the client's network drops and the client retries the exact same request with the same `Idempotency-Key`. Notice what happens."
- **Terminal Command:**
  ```powershell
  Invoke-RestMethod -Uri "http://localhost:8080/jobwork/receipts" -Method Post -Headers @{"Content-Type"="application/json"; "Idempotency-Key"="demo-receipt-001"} -Body $body | ConvertTo-Json
  ```
- **Narration:**
  > "The API returns the original receipt record. In PostgreSQL, unique constraint on `idempotency_key` and row-level issue locking ensure zero duplicate receipts and zero duplicate vouchers are created."

---

### 1:15 – 1:55 (40s) — Preventing Concurrent Overshoot
- **Narration:**
  > "What if ten concurrent receipt requests hit the server simultaneously for an issue with only 40 items outstanding?"
- **Terminal Action:** Run concurrent test / script firing 10 simultaneous requests for 10 units each (100 units requested, 40 available):
  ```powershell
  go test -v -run TestConcurrentReceiptsCannotOvershoot ./cmd/api
  ```
- **Visual:** Terminal shows 4 requests accepted (201 Created), 6 rejected (409 Conflict), total received strictly equals 40.
- **Narration:**
  > "PostgreSQL serializes receipt writers with `SELECT qty FROM jobwork_issues WHERE id=$1 FOR UPDATE`. The total received never overshoots the issued quantity."

---

### 1:55 – 2:40 (45s) — Transient Failures, Duplicate 409 Reconciliation & Manual Retry
- **Visual:** On `/sync` dashboard, show a voucher in the **Failed** column with attempts = 6 and failure reason: `500 Internal Server Error (simulated failure)`.
- **Narration:**
  > "When the remote accounting server fails with 500 errors, the background worker uses exponential backoff. If it exhausts 6 attempts, it marks the voucher 'failed' and displays the failure reason. Let's trigger a manual retry."
- **Visual Action:** Click the **Retry** button in the UI on the failed card.
- **Visual Result:** Button shows `Retrying…`, voucher status immediately resets to `Pending`. The background worker claims it, sends the request to accounting, and reconciles the response. The card transitions from `Failed` to `Pushed` with its external reference!
- **Terminal Verification (Optional quick check):**
  ```powershell
  Invoke-RestMethod -Uri "http://localhost:8080/vouchers?status=failed"
  ```

---

### 2:40 – 3:00 (20s) — Worker Crash Recovery & Concurrency Architecture
- **Visual:** Switch back to code/architecture diagram briefly.
- **Narration:**
  > "How is crash recovery guaranteed? Workers lease vouchers using `FOR UPDATE SKIP LOCKED` with a 15-second `locked_until` lease. If a worker process is killed mid-HTTP request, the lease expires automatically, allowing a surviving or restarted worker to claim it. Because the voucher reuses a stable `client_ref`, the accounting service returns HTTP 409 with the original `external_ref`, which our worker reconciles safely into `pushed`. Two independent worker instances can run simultaneously without duplicate accounting entries."
- **Sign-off:**
  > "All tests pass against real PostgreSQL on Neon. Thank you!"

---

## Production / Recording Tips
- Keep browser zoom at 110% or 125% for high readability in video.
- Terminal font: Cascadia Code / Consolas 14pt+.
- No audio clipping; speak at an even, confident pace.
