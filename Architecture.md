# Architecture

- **Backend:** Go HTTP services using pgx and Postgres/Neon.
- **Frontend:** Next.js App Router, client-side dashboard at `/sync`.
- **Data:** schema migrations in `migrations/`; all correctness constraints live in Postgres-backed transactions.
- **Worker:** leases eligible pending vouchers with `FOR UPDATE SKIP LOCKED`, calls accounting with a 2-second timeout, then backs off or reconciles a duplicate.

Routes: API `POST /jobwork/receipts`, `GET /issues/{id}`, `GET /vouchers`, `POST /vouchers/{id}/retry`; mock `POST /api/vouchers`; UI `/sync`.

The voucher `client_ref` is durable and unique. The accounting mock treats it as its own idempotency key.
