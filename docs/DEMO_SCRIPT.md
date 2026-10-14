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
5. Look up the seeded issue ids (they are random UUIDs, different on every database) and set them in the terminal:
   ```powershell
   & psql "$env:DATABASE_URL" -c "SELECT i.id, s.code, i.qty FROM jobwork_issues i JOIN skus s ON s.id=i.sku_id ORDER BY s.code"
   $issue = "<SR-101 id>"; $issue4 = "<SR-104 id>"
   ```
6. **Prepare a failed voucher before recording.** With the default 20% failure rate a voucher needs six consecutive 500s to fail (about 0.006%), so it will not happen on its own. Start the mock with `$env:MOCK_MODE="deterministic-fail"`, create one receipt, wait ~70 s until it shows under **Failed**, then restart the mock with `$env:MOCK_MODE="deterministic-success"` (or unset it for random mode) so the on-camera retry succeeds.
7. For the duplicate segment, keep the mock terminal visible: its log prints `Duplicate client_ref ... returning 409` when a duplicate is rejected.

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
  $body = "{""issue_id"":""$issue"",""qty"":10,""received_on"":""2026-10-12T12:00:00Z""}"
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
- **Terminal Action:** Fire 10 simultaneous requests for 10 units each against SR-104 (100 units requested, 40 available). In Git Bash, with `ISSUE` set to the SR-104 id:
  ```bash
  for i in $(seq 1 10); do curl -s -o /dev/null -w "%{http_code}
" -X POST localhost:8080/jobwork/receipts -H "Content-Type: application/json" -H "Idempotency-Key: demo-conc-$i" -d "{\"issue_id\":\"$ISSUE\",\"qty\":10,\"received_on\":\"2026-10-14T10:00:00Z\"}" & done; wait
  curl -s localhost:8080/issues/$ISSUE
  ```
- **Visual:** Terminal shows four `201` and six `409`; the issue shows `received: 40, outstanding: 0`. (This was run live on 2026-10-14 and produced exactly 4/6.) The automated test `TestConcurrentReceiptsCannotOvershoot` proves the same thing with 10 requests of qty 1 against 5 issued.
- **Narration:**
  > "PostgreSQL serializes receipt writers with `SELECT qty FROM jobwork_issues WHERE id=$1 FOR UPDATE`. The total received never overshoots the issued quantity."

---

### 1:55 – 2:40 (45s) — Transient Failures, Duplicate 409 Reconciliation & Manual Retry
- **Visual:** On `/sync` dashboard, show the voucher prepared in setup step 6 in the **Failed** column with attempts = 6 and failure reason: `500 Internal Server Error (simulated accounting failure)`.
- **Narration:**
  > "When the remote accounting server fails with 500 errors, the background worker uses exponential backoff. If it exhausts 6 attempts, it marks the voucher 'failed' and displays the failure reason. Let's trigger a manual retry."
- **Visual Action:** Click the **Retry** button in the UI on the failed card.
- **Visual Result:** Button shows `Retrying…`, the voucher moves to `Pending`, the worker claims it within about a second, and the card moves to `Pushed` with an external reference and attempts = 7 (the counter is history and is not reset).
- **Terminal Verification (Optional quick check):**
  ```powershell
  Invoke-RestMethod -Uri "http://localhost:8080/vouchers?status=failed"
  ```

---

### 2:40 – 3:00 (20s) — Worker Crash Recovery & Duplicate Rejected
- **Visual (recommended, since the brief asks to see a duplicate rejected):** Point at the mock terminal's `Duplicate client_ref ... returning 409` line and the worker's `Reconciled duplicate 409` line. These appear whenever a 5-second hang times out and is retried, or after killing and restarting the worker during a hang (start the mock with `MOCK_MODE=deterministic-delay` to make every call hang). After a restart, the voucher stays pending for up to 15 s until the old lease expires.
- **Narration:**
  > "How is crash recovery guaranteed? Workers lease vouchers using `FOR UPDATE SKIP LOCKED` with a 15-second `locked_until` lease. If a worker process is killed mid-HTTP request, the lease expires automatically, allowing a surviving or restarted worker to claim it. Because the voucher reuses a stable `client_ref`, the accounting service returns HTTP 409 with the original `external_ref`, which our worker reconciles safely into `pushed`. Two independent worker instances can run simultaneously without duplicate accounting entries."
- **Sign-off:**
  > "The integration tests run against real PostgreSQL on Neon. Thank you!"

---

## Production / Recording Tips
- Keep browser zoom at 110% or 125% for high readability in video.
- Terminal font: Cascadia Code / Consolas 14pt+.
- No audio clipping; speak at an even, confident pace.
