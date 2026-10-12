# PRD

## Product
**Name:** Sutra Mini. **Description:** A reliable accounting-sync layer for job-work receipts.

## Goals
- Persist receipt and pending voucher atomically.
- Prevent received quantity from exceeding issued quantity under concurrent requests.
- Make receipt replay safe through a database idempotency key.
- Deliver vouchers safely despite slow, failing, duplicate-aware accounting API responses.
- Provide a plain `/sync` dashboard.

## Acceptance criteria
- Go API, Neon-compatible Postgres migrations, mock accounting endpoint, worker, and Next.js UI are present.
- Two real database integration tests cover concurrent overshoot and idempotency replay.
- No secrets are committed and `.env.example` exists.

## Non-goals
- Prices/rates were absent from the brief, so receipt vouchers carry `amount_paise=0` rather than inventing a value.
- `/ask` stretch goal is omitted.
