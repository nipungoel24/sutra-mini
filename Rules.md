# Rules

- Keep receipt/voucher creation in one database transaction.
- Enforce idempotency and inventory limits in Postgres, not process memory.
- Store money in paise; do not use floats.
- Propagate request contexts and set explicit outbound HTTP timeouts.
- Do not commit secrets; use `.env` locally and `.env.example` for names only.
- Do not claim external setup, recording, tests, or deployment without evidence.
