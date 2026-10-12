CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE TABLE skus (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), code text NOT NULL UNIQUE, name text NOT NULL, image_url text NOT NULL);
CREATE TABLE parties (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL, phone text NOT NULL);
CREATE TABLE jobwork_issues (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), party_id uuid NOT NULL REFERENCES parties(id), sku_id uuid NOT NULL REFERENCES skus(id), qty integer NOT NULL CHECK(qty>0), issued_on date NOT NULL);
CREATE TABLE jobwork_receipts (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), issue_id uuid NOT NULL REFERENCES jobwork_issues(id), qty integer NOT NULL CHECK(qty>0), received_on date NOT NULL, idempotency_key text NOT NULL UNIQUE, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE vouchers (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), type text NOT NULL, source_id uuid NOT NULL, amount_paise bigint NOT NULL CHECK(amount_paise>=0), status text NOT NULL CHECK(status IN ('pending','pushed','failed')), attempts integer NOT NULL DEFAULT 0, last_error text, client_ref text NOT NULL UNIQUE, external_ref text, next_attempt_at timestamptz NOT NULL DEFAULT now(), locked_until timestamptz, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(type,source_id));
CREATE INDEX vouchers_ready_idx ON vouchers(status,next_attempt_at) WHERE status='pending';
CREATE INDEX receipts_issue_idx ON jobwork_receipts(issue_id);
