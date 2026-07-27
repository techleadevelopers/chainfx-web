-- Mobile base schema required before schema_phase5.sql on cloud databases.
-- Safe to re-run.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS users (
  id                   UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
  email                TEXT          NOT NULL UNIQUE,
  phone                TEXT,
  full_name            TEXT,
  password_hash        TEXT          NOT NULL,
  wallet_address       TEXT,
  pix_key              TEXT,
  kyc_status           VARCHAR(32)   NOT NULL DEFAULT 'pending',
  kyc_documents        TEXT,
  pin_hash             TEXT,
  biometry_enabled     BOOLEAN       NOT NULL DEFAULT false,
  two_factor_enabled   BOOLEAN       NOT NULL DEFAULT false,
  two_factor_secret    TEXT,
  refresh_token_hash   TEXT,
  deleted_at           TIMESTAMPTZ,
  created_at           TIMESTAMPTZ   NOT NULL DEFAULT now(),
  updated_at           TIMESTAMPTZ   NOT NULL DEFAULT now()
);

ALTER TABLE users ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_active_email ON users(email) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_users_deleted_at ON users(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_users_wallet ON users(wallet_address) WHERE wallet_address IS NOT NULL;

CREATE TABLE IF NOT EXISTS devices (
  id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id      UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  device_name  TEXT,
  device_type  TEXT,
  fcm_token    TEXT,
  apns_token   TEXT,
  last_active  TIMESTAMPTZ,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_devices_push_unique
  ON devices(user_id, COALESCE(fcm_token,''), COALESCE(apns_token,''));
CREATE INDEX IF NOT EXISTS idx_devices_user ON devices(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS notifications (
  id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title       TEXT        NOT NULL,
  body        TEXT,
  type        TEXT,
  read        BOOLEAN     NOT NULL DEFAULT false,
  data        TEXT,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_notifications_user ON notifications(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notifications_unread ON notifications(user_id, read) WHERE read = false;

CREATE TABLE IF NOT EXISTS settings (
  user_id                 UUID          PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  dark_mode               BOOLEAN       NOT NULL DEFAULT true,
  language                VARCHAR(8)    NOT NULL DEFAULT 'pt-BR',
  currency                VARCHAR(8)    NOT NULL DEFAULT 'BRL',
  notifications_enabled   BOOLEAN       NOT NULL DEFAULT true,
  daily_limit             NUMERIC(18,2) NOT NULL DEFAULT 10000,
  updated_at              TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS operation_ids (
  operation_id   TEXT        NOT NULL,
  user_id        UUID        NOT NULL,
  operation_type TEXT        NOT NULL,
  status         TEXT        NOT NULL DEFAULT 'pending',
  result_ref     TEXT,
  completed_at   TIMESTAMPTZ,
  expires_at     TIMESTAMPTZ NOT NULL DEFAULT now() + INTERVAL '24 hours',
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (operation_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_operation_ids_expires_at ON operation_ids(expires_at);
CREATE INDEX IF NOT EXISTS idx_operation_ids_status ON operation_ids(status);

ALTER TABLE orders ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_orders_user ON orders(user_id, created_at DESC) WHERE user_id IS NOT NULL;

ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS provider VARCHAR(32) NOT NULL DEFAULT 'generic';
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS target_url TEXT;
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS events TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS secret TEXT;
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS description TEXT;
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS last_triggered_at TIMESTAMPTZ;
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS last_status_code INT;
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS last_error TEXT;
ALTER TABLE webhook_subscriptions ADD COLUMN IF NOT EXISTS failure_count INT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_webhook_subs_user ON webhook_subscriptions(user_id);
