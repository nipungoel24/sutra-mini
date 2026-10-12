# Phases

- [x] Phase 0: Read brief, audit workspace, inspect environment and dependencies.
- [x] Phase 1: Create schema, migrations, API, accounting mock, and lease-based worker.
- [x] Phase 2: Create plain accessible `/sync` dashboard.
- [x] Phase 3: Setup Go 1.23+, provision Neon databases (`sutra` and `sutra_test`), configure `.env`.
- [x] Phase 4: Fix PostgreSQL UUID type casting, concurrency timeouts, and connection pool sizing.
- [x] Phase 5: Run real PostgreSQL integration tests (Concurrent overshoot & Idempotency replay).
- [x] Phase 6: Expand worker integration tests (Lease recovery, 409 duplicate reconciliation, exhaustion to failed, 2 concurrent workers).
- [x] Phase 7: Complete Next.js dependencies and verify production build (`pnpm run build`).
- [x] Phase 8: End-to-end verification (Receipt creation, replay, 10 concurrent requests, worker sync, retry).
- [x] Phase 9: Clean-machine documentation (`README.md`, `AGENT_NOTES.md`, `docs/VERIFICATION.md`, `docs/REQUIREMENTS_AUDIT.md`, `docs/DEMO_SCRIPT.md`).
- [x] Phase 10: Git working tree audit, secret sanitization, and demo preparation.
- [ ] Phase 11: Push to public GitHub repository (Awaiting user authorization for remote).
