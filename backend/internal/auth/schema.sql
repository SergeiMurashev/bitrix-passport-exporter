CREATE TABLE IF NOT EXISTS auth_users (
  id BIGSERIAL PRIMARY KEY,
  login TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS export_audit_logs (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NULL REFERENCES auth_users(id) ON DELETE SET NULL,
  user_login TEXT NOT NULL DEFAULT '',
  client_ip TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  mode TEXT NOT NULL DEFAULT '',
  format TEXT NOT NULL DEFAULT '',
  success BOOLEAN NOT NULL DEFAULT FALSE,
  error_text TEXT NOT NULL DEFAULT '',
  duration_ms BIGINT NOT NULL DEFAULT 0,
  deals_total INT NOT NULL DEFAULT 0,
  tasks_total INT NOT NULL DEFAULT 0,
  deals_with_support INT NOT NULL DEFAULT 0,
  support_measures_total INT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_export_audit_logs_created_at ON export_audit_logs (created_at DESC);
