# Memory

## Current phase
Phase 11: Final Quality Gate and Submission Preparation complete. All deliverables verified with evidence.

## Verified facts
- Go 1.23.6 installed and verified on Windows x64.
- Neon databases `sutra` (runtime) and `sutra_test` (isolated integration testing) provisioned and verified.
- Schema migrations `001_init.sql` and seed data `002_seed.sql` successfully applied on Neon.
- Database contains 5 SKUs with Unsplash images, 2 job-worker parties, and 4 open issues.
- `cmd/api`, `cmd/mock-accounting`, `cmd/worker` compile with zero errors.
- Integration tests `TestConcurrentReceiptsCannotOvershoot` and `TestIdempotencyReplayCreatesNothingTwice` pass against PostgreSQL on Neon.
- Worker integration tests `TestWorkerDuplicateReconciliation`, `TestWorkerLeaseReclaimAfterCrash`, `TestWorkerExhaustionToFailed`, and `TestTwoWorkersConcurrentProcessing` pass against PostgreSQL on Neon.
- Next.js production build succeeds with Turbopack (`cd web && pnpm run build`) with zero TypeScript errors.
- End-to-end verified: receipt creation, idempotency replay, 10 concurrent requests preventing overshoot, worker synchronization, duplicate 409 reconciliation, exponential backoff, manual retry, and 2 simultaneous workers.
- No secrets committed; `.env` is ignored by `.gitignore`.

## Key decisions
- PostgreSQL row lock `SELECT qty FROM jobwork_issues WHERE id = $1::uuid FOR UPDATE` guarantees zero overshoot.
- Atomic single database transaction for receipt and voucher.
- Database unique constraint on `idempotency_key` guarantees replay safety.
- Worker leases (`FOR UPDATE SKIP LOCKED`, 15-second lease) ensure crash safety without blocking database connection pools.
- Stable client reference (`receipt:<receipt_id>`) and accounting 409 duplicate reconciliation ensure idempotent push delivery.
