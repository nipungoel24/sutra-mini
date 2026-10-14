# Independent Code Audit: Sutra Mini

**Reviewer:** Claude Code (Anthropic), acting as an independent reviewer, not the implementer
**Date:** 2026-10-14
**Baseline:** `master` @ `b6154d5`, working tree clean, no remote configured
**Authoritative brief:** `case_study_brief.pdf` (2 pages), read in full. Not in the repo; found in the candidate's Downloads folder.
**Environment:** Windows 11, Go 1.23.6, Node 22.20.0, pnpm 11.22.0, psql 18, Neon PostgreSQL (`sutra` runtime, `sutra_test` tests)

---

## A. Overall Verdict

**READY AFTER MINOR FIXES**

The fixes are already applied and verified in the working tree but are **not committed**. Once the candidate reviews and commits them, the repository is ready to publish.

No P0 defect was found. The core guarantees in the brief hold, and I checked them by running the code, not by reading the reports:
- no overshoot under concurrent receipts
- receipt and voucher are created in one transaction
- idempotency is enforced by Postgres
- no duplicate accounting entry after a crash
- two workers are safe

Six P1 issues and several P2 issues were found. All P1s were fixed with narrow changes and covered by tests or live runs.

---

## B. Requirements Audit

Evidence key: **T** = automated integration test against real Postgres, **L** = live run of the real binaries on 2026-10-14, **C** = code inspection.

### Database
| Requirement | Where | Evidence | Status |
| :--- | :--- | :--- | :--- |
| skus, parties, jobwork_issues, jobwork_receipts, vouchers tables | `migrations/001_init.sql` | C, T (all tests use them) | PASS |
| 5 SKUs with image URLs, 2 job-workers, 4 open issues | `migrations/002_seed.sql` | L (seeded `sutra_test`, queried 4 issues: 100/80/60/40) | PASS |
| Neon-compatible migrations | `001_init.sql` uses `pgcrypto`/`gen_random_uuid()` | L (applied on Neon) | PASS |
| Money in integer paise | `amount_paise bigint CHECK >= 0`, Go `int64` | C | PASS (amount is always 0 because the brief gives no rates; documented) |
| Postgres-enforced uniqueness | `idempotency_key UNIQUE`, `client_ref UNIQUE`, `UNIQUE(type, source_id)` | C, T | PASS |

### API
| Requirement | Where | Evidence | Status |
| :--- | :--- | :--- | :--- |
| `POST /jobwork/receipts` never overshoots under concurrency | `createReceipt`: `SELECT ... FOR UPDATE` on the issue row, then `SUM` + check + insert in one tx | T `TestConcurrentReceiptsCannotOvershoot` (mutation-checked), L (10×qty10 vs 40 → four 201, six 409, outstanding 0) | PASS |
| Voucher created in the same transaction | both inserts in one `pgx.Tx`, single `Commit` | C, T (asserts receipts = vouchers = 5, no orphans) | PASS |
| Idempotency-Key replay returns the same result and creates nothing | key lookup before and after the lock + `UNIQUE` index | T `TestIdempotencyReplayCreatesNothingTwice` (now also asserts identical ids; mutation-checked), L | PASS |
| `GET /issues/{id}` issued / received / outstanding | `getIssue` | L | PASS (no automated test) |
| `GET /vouchers?status=` | `listVouchers` | L, used by `/sync` | PASS (no automated test) |
| `POST /vouchers/{id}/retry` | `retryVoucher` | L (UI click: failed → pushed) | PASS (no automated test) |
| Context and timeouts | receipt tx uses `r.Context()` + 30 s | C | PASS |

### Mock accounting
| Requirement | Evidence | Status |
| :--- | :--- | :--- |
| `POST /api/vouchers` | C, L | PASS |
| 20% HTTP 500 | C (`n < 20`), L (500s seen in random mode) | PASS |
| 10% 5 s hang, then success | C (`20 <= n < 30`; entry recorded *before* sleeping), L | PASS |
| Duplicate `client_ref` → 409 with original `external_ref` | C (checked under mutex before the random branch), L | PASS |

### Worker
| Requirement | Evidence | Status |
| :--- | :--- | :--- |
| Safe claiming | `FOR UPDATE SKIP LOCKED` + `locked_until` lease, committed before HTTP. T `TestTwoWorkersConcurrentProcessing` (strengthened, mutation-checked) | PASS |
| Retry with backoff | `1 << attempts` s. L log: 2, 4, 8, 16, 32 s | PASS |
| Ends `failed` with a reason | T `TestWorkerExhaustionToFailed`, L (`last_error` = "500 Internal Server Error (simulated accounting failure)") | PASS |
| Manual retry | L | PASS (grants one extra attempt; documented) |
| Explicit HTTP timeouts | `context.WithTimeout` 2 s + `http.Client{Timeout: 2.5s}`. L: `context deadline exceeded` | PASS |
| Lease expiry and recovery | T `TestWorkerLeaseReclaimAfterCrash`, L (reclaimed ~15 s after the kill) | PASS |
| Two instances at once | T, L (two processes: claims 8/7, each of 12 refs accepted exactly once) | PASS |
| Never posted twice across a crash between HTTP and DB update | T `TestWorkerCrashMidFlightReconcilesDuplicate`, L (hard kill during the 5 s hang → restart → 409 → pushed, one mock entry per ref) | PASS |
| Stale worker cannot overwrite newer state | **Was missing**; fixed. T `TestStaleWorkerResultIsDiscarded` (mutation-checked) | PASS (after fix) |
| 5 s hang does not block others | one goroutine per claimed voucher. L (`deterministic-delay`: other vouchers progressed during hangs) | PASS |

### Frontend
| Requirement | Evidence | Status |
| :--- | :--- | :--- |
| `/sync` grouped by status, attempts, failure reason, Retry | L (rendered against the real API; Retry worked) | PASS |
| Loading / empty / error states | C (`Loading vouchers...`, `No X vouchers.`, `role="alert"` banner) | PASS |
| Duplicate clicks | C (`busy` guard + disabled button) | PASS |
| Responsive | L (375 px: single column, `scrollWidth` = viewport width, no horizontal scroll) | PASS |
| No console errors | L (`read_console_messages`: none) | PASS |
| Production build | `pnpm run build` exit 0 | PASS |

### Submission
| Item | Status |
| :--- | :--- |
| README reproducible from a clean machine | PASS after fixes (it previously hard-coded a Neon-only UUID; see E4) |
| AGENT_NOTES.md candid | PASS (review section added) |
| `.env.example` placeholders only | PASS |
| At least two meaningful tests | PASS (9 tests; the 2 required ones mutation-checked) |
| No committed secrets | PASS (see C) |
| Public GitHub repo | NOT DONE (user task) |
| 3-minute recording | NOT DONE (user task; script corrected) |
| `/ask` stretch | NOT APPLICABLE (optional, omitted) |

---

## C. Security Audit

What I checked:
- `.env` is ignored (`.gitignore:1`) and **has never been committed**: `git log --all -- .env` is empty.
- Every commit was scanned (`git grep` across all 3 commits, since no gitleaks/trufflehog was installed) for Neon `npg_` passwords, credentialed `postgres://` URLs, `sk-`/`ghp_`/`github_pat_`/AWS keys, private keys, and bearer tokens. The only matches are placeholders: `postgres:postgres@localhost` in `.env.example` and `user:password@ep-xyz...` in the README.
- The real Neon endpoint ID and password from `.env` were searched for in every commit without printing them: **0 matches**.
- API error responses no longer include raw database errors. Before the fix, `lock issue: ERROR: invalid input syntax for type uuid ... (SQLSTATE 22P02)` and `create voucher: <pg error>` were returned to callers.
- CORS: the old code reflected any request `Origin` and ignored `CORS_ORIGIN`. It now sends `CORS_ORIGIN` (default `*`). No cookies or credentials are used, so `*` is acceptable for local use.
- Request body capped at 1 MiB.
- There is no authentication. That is appropriate for a local case-study service, but it is **not production-hardened**, and the README now says so.
- Test-safety: both test helpers refuse to run when `TEST_DATABASE_URL == DATABASE_URL`, because they `TRUNCATE`.

Conclusion: based on these checks the repository is safe to publish. This is not a guarantee of absolute security. No credential rotation is needed, since nothing leaked into history.

---

## D. Test Results

All commands were run by me on 2026-10-14. DB-backed tests used only `sutra_test`, with `DATABASE_URL` unset in that shell.

| # | Command | Exit | Result / evidence |
| :--- | :--- | :--- | :--- |
| 1 | `git status`, `git log -5`, `git remote -v`, `git diff --check` | 0 | HEAD `b6154d5`, clean, no remote, no whitespace errors |
| 2 | `go build ./...` (baseline) | 0 | compiles |
| 3 | `go vet ./...` (baseline) | 0 | clean |
| 4 | `gofmt -l .` (baseline) | 0 | **listed 3 files** (`cmd/api/main.go`, both test files) |
| 5 | `go test -v -p 1 -count=1 ./...` (baseline) | 0 | 7/7 PASS (api 38.9 s, worker 32.8 s) |
| 6 | Mutation: remove `FOR UPDATE` → overshoot test | 1 | **FAIL** "created 6 receipts, want 5" (test is meaningful) |
| 7 | Mutation: disable idempotency hit path → replay test | 1 | **FAIL** "replay: 500" |
| 8 | Edge-case probe on original code | 0 | malformed issue UUID → **500 leaking SQLSTATE**; `GET /issues/xyz` → 500; retry bad id → 500; retry unknown id → 409; whitespace key accepted; empty list → `null` |
| 9 | `go build ./... && go vet ./... && gofmt -l cmd` (after fixes) | 0 | clean, no files listed |
| 10 | `go test -v -p 1 -count=1 ./...` (after fixes) | 0 | **9/9 PASS**: api 3 tests 43.6 s, worker 6 tests 38.5 s |
| 11 | Mutation: neutralise result-write fence → `TestStaleWorkerResultIsDiscarded` | 1 | **FAIL** "stale failure overwrote settled voucher: status=failed" (confirms the original defect) |
| 12 | Mutation: drop lease condition → `TestTwoWorkersConcurrentProcessing` | 1 | **FAIL** (pushed=1, pending=5) |
| 13 | `cd web && pnpm run build` | 0 | Next 16.4.0 Turbopack, TypeScript OK, `/sync` static |
| 14 | Live: replay same key | — | 201 then 200, identical `id` and `voucher_id` |
| 15 | Live: 10 concurrent qty=10 vs issue of 40 | — | four 201, six 409; issue 40/40/0 |
| 16 | Live: `MOCK_MODE=deterministic-delay`, hard-kill worker (`taskkill /F`) while mock logs `Simulating 5s delay`, restart | — | 3 vouchers accepted remotely but still `pending` locally; after restart all reconciled via 409 (`Reconciled duplicate 409`); mock shows **1 accepted entry per client_ref** |
| 17 | Live: two worker processes, random mock, 12 receipts | — | claims 8 / 7; 12 refs each accepted once; 17/17 pushed with distinct `external_ref`s; 0 stale discards |
| 18 | Live: `deterministic-fail` → exhaustion → UI Retry with `deterministic-success` | — | backoff 2/4/8/16/32 s, `failed` at attempt 6 with reason; Retry click → `pushed` at attempt 7 |
| 19 | Browser: `/sync` at 375 px + console | — | single column, no horizontal scroll, no console errors |

Note on test command: `-p 1` **is genuinely required**. `cmd/api` truncates every table and `cmd/worker` truncates `vouchers` in the same database, so running the packages in parallel races.

---

## E. Critical Findings

| ID | Sev | File | Finding | Fixed? |
| :--- | :--- | :--- | :--- | :--- |
| E1 | P1 | `cmd/worker/main.go` | Result writes were not fenced: a stale worker could overwrite a reclaimed or settled voucher | Yes |
| E2 | P1 | `cmd/worker/worker_integration_test.go` | Two-worker test could not detect a double claim | Yes |
| E3 | P1 | `cmd/api/main.go` | Malformed UUIDs → HTTP 500 with raw SQL error leaked | Yes |
| E4 | P1 | `README.md`, `docs/DEMO_SCRIPT.md` | Clean-machine instructions used a UUID that exists only on the candidate's Neon DB; no way given to find issue ids; `sutra_test` not created for Docker | Yes |
| E5 | P1 | `docs/DEMO_SCRIPT.md` | Recording plan not achievable as written | Yes |
| E6 | P1 | `docs/REQUIREMENTS_AUDIT.md` | Cited automated tests that do not exist | Yes |
| E7 | P2 | `cmd/worker/main.go` | `drain` returned `nil` on any DB error | Yes |
| E8 | P2 | `cmd/api/main.go` | Misc. API hygiene | Yes |
| E9 | P2 | repo | gofmt not applied | Yes |
| E10 | P2 | various | Documented limitations, not changed | No (documented) |

**E1: stale-worker overwrite (P1).**
- *Reproduce:* a voucher is reclaimed or settled by worker B (attempts 7, `pushed`). Stale worker A, still holding attempt 6, calls `handleFailure`. The old `UPDATE ... WHERE id=$1` set it to `failed`. Mutation run #11 shows this exact outcome.
- *Impact:* the voucher state is wrong. A manual retry would recover it through 409, so there is no duplicate entry, but the brief scores this exact race. It needs a worker slower than its 15 s lease (paused process, very slow DB), so it is rare but real.
- *Fix:* all three result `UPDATE`s add `AND status='pending' AND attempts=$n`. `attempts` is incremented on every claim, so it acts as a fencing token. When zero rows are affected, a "Discarded stale result" line is logged. New test `TestStaleWorkerResultIsDiscarded`.

**E2: two-worker test did not prove its claim (P1).**
- *Problem:* the mock returned the same `external_ref` for every call, and the test only counted `pushed` rows. Two deliveries of one voucher would still pass.
- *Fix:* the test mock now counts deliveries per `client_ref` and asserts exactly 1 each, plus `max(attempts) = 1`.

**E3: malformed id → 500 + SQL leak (P1).**
- *Reproduce:* `POST /jobwork/receipts` with `"issue_id":"not-a-uuid"` → 500 `lock issue: ERROR: invalid input syntax for type uuid ... (SQLSTATE 22P02)`. `GET /issues/xyz` and `POST /vouchers/xyz/retry` → 500.
- *Fix:* UUID validation returns 400 (body) or 404 (path), and error bodies are now generic, with details logged server-side. Covered by `TestReceiptRejectsInvalidInputWithoutLeakingSQL`, which also checks blank keys, bad JSON, and `qty=0`.

**E4: README not reproducible as written (P1).**
- *Problem:* the examples used `ea0a0710-...`, which is a random UUID from the candidate's own Neon seed. Docker users were never told to create `sutra_test`.
- *Fix:* added a `psql` query to list issue ids, `CREATE DATABASE sutra_test` for Docker, the optional env vars (`MOCK_MODE`, `CORS_ORIGIN`, `ACCOUNTING_ADDR`), a status-code table, and a new section 6a with step-by-step instructions for each evaluator scenario (replay, ten concurrent, kill during hang, two workers, failed + retry).

**E5: demo script not achievable (P1).**
- *Problems:*
  - It expected a naturally occurring failed voucher. In random mode that needs six consecutive 500s, about 0.2⁶ ≈ 0.006%.
  - It claimed `go test -run TestConcurrentReceiptsCannotOvershoot` shows "4 accepted, 6 rejected, 40". The test actually runs 10 × qty 1 against 5 issued, so it shows 5/5.
  - It hard-coded the same UUID.
  - The brief's "duplicate rejected" moment was narration only.
- *Fix:* added prep steps (`MOCK_MODE=deterministic-fail`, then `deterministic-success`), a live curl loop with the correct expected 4/6 (verified live), and pointers to the mock's `409` log line.

**E6: inflated requirements audit (P1, honesty).** Rows cited "Mock test suite", "Timeout test", "Async dispatch test", and an integration test for `GET /issues`, none of which exist. A recruiter reading the repo would catch this. The rows are corrected to say "manual / live run", and a correction note was added.

**E7: swallowed worker errors (P2).** `drain` now returns `claim voucher: %w`, which the main loop logs.

**E8: API hygiene (P2).**
- the empty list returned `null`, now `[]`
- `rows.Scan` and `rows.Err` errors were ignored
- retry on an unknown id returned 409, now 404
- whitespace-only Idempotency-Key was accepted
- no body-size limit
- unique-violation detection used `strings.Contains(err.Error(), "idempotency")`, now `pgconn.PgError` code `23505`
- receipt JSON keys were PascalCase (`ID`, `IssueID`), now snake_case like the rest of the API
- dead `var _ = strconv.Itoa` removed

**E9: formatting (P2).** `cmd/api/main.go` was written as 300-character one-liners, which is hard to read for the brief's "small handlers / understand every line" criterion and fails `gofmt -l`. I ran `gofmt -w cmd/`. That only changes whitespace and line breaks, and the suite passed afterwards.

**E10: documented, not changed (P2 / design choices).**
- Same key with a different body returns the original receipt; the body is not fingerprinted.
- Manual retry keeps the attempt history, so it grants one attempt.
- A timeout on the 6th attempt can mark `failed` a voucher that accounting accepted. A manual retry settles it via 409.
- On Ctrl+C, in-flight pushes are abandoned rather than awaited. This is safe through lease + 409.
- The mock's `client_ref` memory is in-process, so restarting the mock forgets it.
- Frontend polling can overlap if a request takes over 4 s, and a retry error message is cleared by the next successful poll. Both are cosmetic.

**Crash test assessment (brief phase 5).** `TestWorkerCrashMidFlightReconcilesDuplicate` genuinely exercises "remote accepted, local uncommitted":
- Worker A's claim and HTTP call are reproduced in test code. A real crash can't be injected inside `push`, and this is a faithful simulation.
- Worker B runs the **production** `drain` → `push` → 409 path.
- The test asserts one remote entry and two calls. It is deterministic: the lease is expired by SQL, not by sleeping.
- *Limitation:* the remote side is an in-test mock with the same 409 rule as `cmd/mock-accounting`, not that binary. The live run (#16) closes this gap against the real mock.
- Malformed 409s are handled correctly: a 409 without `external_ref` goes to `handleFailure` and is never marked pushed. Any 409 *with* a non-empty `external_ref` is trusted, which is inherent to the 409 contract.

---

## F. File Changes

| File | Reason |
| :--- | :--- |
| `cmd/api/main.go` | E3, E8, E9 (UUID validation, no SQL leaks, body limit, trimmed key, `pgconn` 23505, `[]` list, scan errors, retry 404, CORS honours env, snake_case JSON, gofmt) |
| `cmd/api/receipt_integration_test.go` | DB-safety guard; overshoot test also asserts no orphan receipts or vouchers; replay test asserts identical ids; new `TestReceiptRejectsInvalidInputWithoutLeakingSQL`; setup errors checked; gofmt |
| `cmd/worker/main.go` | E1 (fenced result writes, `markPushed`, stale-discard log), E7 |
| `cmd/worker/worker_integration_test.go` | DB-safety guard; E2; new `TestStaleWorkerResultIsDiscarded` |
| `README.md` | E4, test list and `-p 1` rationale, limitations |
| `docs/DEMO_SCRIPT.md` | E5 |
| `docs/REQUIREMENTS_AUDIT.md` | E6 |
| `docs/VERIFICATION.md` | appended a re-verification section and corrections |
| `AGENT_NOTES.md` | added the Claude Code review as a tool used plus what it caught |
| `docs/CLAUDE_CODE_AUDIT.md` | this report |

No migrations, dependencies, lockfiles, or frontend source were changed. `web/next-env.d.ts` was rewritten by my `next dev` run and restored to the committed version. Nothing was staged, committed, or pushed.

---

## G. Publication Readiness

The local repository is ready to push **after the candidate reviews and commits the changes above**. The module path `github.com/nipungoel24/sutra-mini` must match the GitHub repo you create. If you use a different user or repo name, update `go.mod` (no internal imports depend on it). The tracked files include no `.env`, no build output (`bin/`, `.next/`, and `node_modules/` are ignored), no logs, and no local DB files. It has **not** been published.

---

## H. Remaining Manual Tasks

1. Review the diff and commit it.
2. Create the public GitHub repo, add the remote, and push.
3. Record the 3-minute video using the corrected `docs/DEMO_SCRIPT.md`. Prepare the failed voucher first.
4. Reply to the invite email with both links.

---

## I. Likely Technical Interview Questions (from this code)

1. **"Walk me through ten concurrent receipts on one issue."** Each tx does `SELECT qty ... FOR UPDATE`, so they serialize on the issue row. Under READ COMMITTED, each later statement takes a fresh snapshot, so the `SUM` after the lock sees every committed predecessor. Why not SERIALIZABLE (retries needed) or a CHECK/trigger? Why does the second idempotency lookup after the lock work? It is also a fresh snapshot.
2. **"Two requests with the same key but different `issue_id`s arrive together."** They lock different rows, both pass the lookups, and one insert blocks on the other's uncommitted unique-index entry, then fails with 23505 → 409. Also: same key with a different body returns the original (no fingerprint). Would you change that?
3. **"I kill the worker during the 5-second hang. What happens, step by step?"** The mock recorded the entry before sleeping. The voucher is `pending` with `locked_until` set. After a restart it waits up to 15 s for lease expiry, re-sends the same `client_ref`, gets 409 with the original ref, and marks `pushed`. Follow-up: why is the HTTP timeout (2 s) shorter than the hang, and what if the 6th attempt times out?
4. **"What if a worker is slower than its lease?"** `attempts` is the fencing token. Result `UPDATE`s require `status='pending' AND attempts=<claim>`, so late writes match zero rows (`TestStaleWorkerResultIsDiscarded`). Why `context.Background()` for push, and what does Ctrl+C do to in-flight pushes?
5. **Live change candidates:** reset attempts on manual retry, add jitter or a max delay to backoff, make the retry budget configurable, persist the mock's refs, fingerprint idempotent request bodies, or a real `amount_paise` from a rate. Know where each lives (`handleFailure`, `retryVoucher`, mock `store`, `createReceipt`).

---

## J. Final Submission Checklist

**Software deliverables (verified in this review)**
- [x] Source code: API, worker, mock, migrations, `/sync`
- [x] Go build / vet / gofmt clean
- [x] PostgreSQL integration tests: 9/9 PASS on Neon `sutra_test`; required 2 mutation-checked
- [x] Crash recovery: automated test + live kill-during-hang run
- [x] Two-worker concurrency: strengthened test + live two-process run
- [x] Frontend production build
- [x] README (clean-machine fixes applied)
- [x] AGENT_NOTES.md
- [x] `.env.example` placeholders only
- [x] Secret audit across full history
- [ ] Git state: **changes uncommitted, waiting on your review and commit**

**Publication and recording (user tasks, not done)**
- [ ] Public GitHub publication
- [ ] Three-minute recording
