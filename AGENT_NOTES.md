# AGENT_NOTES: Sutra Mini

**Engineer / AI Pair:** Antigravity (Google DeepMind) & Codex (initial scaffold)  
**Organization:** Shree Radha Studio  
**Date:** 2026-10-12  

---

## 1. Tools and AI Systems Used

- **Codex:** Used initially to generate the starter scaffold, migrations, initial handlers, and preliminary Next.js page.
- **Antigravity (Google DeepMind):** Used as the principal software engineer to audit the existing repository, provision tools, diagnose failures, implement reliability guarantees, expand integration test coverage, and verify end-to-end execution.
- **Toolchain:** Official Go 1.23.6 runtime (Windows amd64), Node.js v22.20.0, pnpm 11.22.0, PostgreSQL 18 client tools (`psql`), and Neon serverless PostgreSQL.

---

## 2. Key Implementation Decisions

1. **Transactional Invariant via PostgreSQL Row-Locking:**
   Instead of in-memory sync primitives or distributed locks, we enforce concurrency control directly in PostgreSQL using `SELECT qty FROM jobwork_issues WHERE id = $1::uuid FOR UPDATE`. Both the job-work receipt and the accounting voucher are inserted in that same transaction and committed atomically.
2. **Two-Stage Idempotency Fence:**
   The `Idempotency-Key` is checked twice: first before waiting on the issue lock (to immediately short-circuit replays on already processed receipts), and again immediately after acquiring the issue lock (to handle race conditions where a duplicate request arrived while waiting on the lock). A unique database index on `idempotency_key` serves as the final database-level guarantee against duplicate inserts.
3. **Lease-Based Worker Claiming:**
   The background worker claims vouchers with `FOR UPDATE SKIP LOCKED` and sets `locked_until = now() + interval '15 seconds'` and `attempts = attempts + 1`. This decouples the database row lock from the outbound HTTP request: the database transaction commits immediately upon claiming, so slow or hung HTTP requests do not tie up database connection pools or row locks.
4. **Stable Client References and 409 Reconciliation:**
   "Never posted twice" cannot physically guarantee that a network packet is only sent once if a worker crashes after the accounting service accepts the request. The robust solution is a durable, unique `client_ref` (`receipt:<receipt_id>`). When retrying after a crash or timeout, the accounting mock detects the duplicate and returns `HTTP 409 Conflict` with the original `external_ref`. The worker explicitly validates this payload and marks the voucher as `pushed`.
5. **Zero Monetary Amount (`amount_paise = 0`):**
   The case study brief did not specify item rates or job-work charges. Rather than inventing arbitrary business figures, `amount_paise` is stored as `0` using integer types, adhering strictly to the schema specification.

---

## 3. What Was Initially Incomplete or Broken

When we audited the repository initialized by Codex:
1. **Missing Toolchains:** Go was not installed or discoverable in PATH. The Next.js package installation was incomplete (`web/node_modules` was missing essential binaries like `next` and had an unlinked `.pnpm` virtual store).
2. **Database Untested:** No database was configured or provisioned; migrations had never been executed.
3. **Worker Duplicate Vulnerability:** The worker treated any HTTP 409 blindly as success without validating whether `external_ref` was actually returned. If an upstream service returned a generic 409 error without an external reference, it would have marked the voucher pushed with an empty string.
4. **Tight Timeouts Masking Concurrency:** The API receipt handler had a 5-second context timeout, which failed when 10 serialized transactions executed over cross-continental network latency to Neon.
5. **Worker Integration Tests Missing:** Only 2 receipt tests existed; worker lease recovery, 409 duplicate reconciliation, exhaustion to failed, and two-worker concurrency were untested.

---

## 4. Concrete Failures Caught During Verification & How They Were Fixed

### Failure 1: PostgreSQL UUID Type Inference Mismatch
- **Symptoms:** During our initial run of `go test -v ./cmd/api`, `TestIdempotencyReplayCreatesNothingTwice` failed with `HTTP 500: create voucher`.
- **Root Cause:** In `cmd/api/main.go`, the voucher insert query was:
  ```sql
  INSERT INTO vouchers(type, source_id, amount_paise, status, client_ref)
  VALUES('jobwork_receipt', $1, 0, 'pending', 'receipt:' || $1::text)
  ```
  Because `$1` was also used in `'receipt:' || $1::text`, PostgreSQL and `pgx` inferred the type of `$1` as `text`. However, column `source_id` is of type `uuid`. PostgreSQL rejected the insert with a type mismatch error (`column "source_id" is of type uuid but expression is of type text`).
- **Fix:** Explicitly cast `$1::uuid` in the query:
  ```sql
  INSERT INTO vouchers(type, source_id, amount_paise, status, client_ref)
  VALUES('jobwork_receipt', $1::uuid, 0, 'pending', 'receipt:' || $1::text)
  ```
  We applied this explicit cast across all queries touching UUID columns (`id = $1::uuid`).

### Failure 2: False 404s Under High Concurrency
- **Symptoms:** In `TestConcurrentReceiptsCannotOvershoot`, concurrent requests received `HTTP 404: issue not found`.
- **Root Cause:** When multiple concurrent requests queued up behind the row lock `SELECT qty FROM jobwork_issues ... FOR UPDATE`, the query timed out due to a 5-second context deadline. The error handler:
  ```go
  if err = tx.QueryRow(...).Scan(&issued); err != nil {
      writeError(w, 404, "issue not found")
  }
  ```
  blindly assumed *any* error from `Scan(&issued)` was a missing row, returning a misleading 404 when it was actually a lock wait timeout.
- **Fix:** We adjusted the context deadline to 30 seconds for serialized transaction batches, increased `pgxpool.MaxConns` to 30, and updated the handler to only return 404 when `errors.Is(err, pgx.ErrNoRows)` is true, returning 500 with proper logging for database timeouts.

### Failure 3: Unlinked Frontend Binaries from Interrupted Install
- **Symptoms:** Running `pnpm run build` failed with `'next' is not recognized as an internal or external command`.
- **Root Cause:** A previous `pnpm` run had left an incomplete virtual store in `web/node_modules/.pnpm/`, but the top-level `.bin/next` symlinks were never generated. Running `npm install` subsequently failed with null reference errors inside npm's arborist due to pnpm symlinks.
- **Fix:** We ran `pnpm add next@16.4.0 react@19.3.0 react-dom@19.3.0 typescript@5.7.2` directly using `pnpm`, properly populating the lockfile (`pnpm-lock.yaml`) and generating the Turbopack production build cleanly.

---

## 5. What Remains Unverified or External

- **Public GitHub Remote Push:** The repository has been audited, Git history is clean, and `.env` is uncommitted. Pushing to a public remote requires user authorization and account authentication.
- **Screen Recording:** A demonstration script has been prepared in `docs/DEMO_SCRIPT.md`. The video file itself requires manual screen recording software.
- **Neon Standalone Account Provisioning:** A dedicated database (`sutra` and `sutra_test`) was provisioned on the available Neon cluster; automated provisioning of a separate Neon organization account was not performed.

---

## 6. Honest Statement of Limitations

- The `/ask` LLM stretch endpoint was omitted in accordance with the brief instructions ("Stretch, only if you finished early"). Correctness and testing of core transactional guarantees were prioritized.
- Network latency to serverless Neon (us-east-2) adds ~150ms per round-trip from Windows local testing in India; while local transactions run in single-digit milliseconds, multi-request sequential batches over WAN take several seconds. All pool configurations and timeouts account for this.
