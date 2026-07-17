-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE TYPE user_role AS ENUM ('admin', 'student');
CREATE TYPE user_status AS ENUM ('active', 'disabled');

CREATE TABLE users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  username text NOT NULL,
  display_name text NOT NULL,
  role user_role NOT NULL,
  status user_status NOT NULL DEFAULT 'active',
  password_hash text NOT NULL,
  must_change_password boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  CONSTRAINT users_username_format CHECK (username ~ '^[a-zA-Z0-9._-]{3,64}$')
);
CREATE UNIQUE INDEX users_username_active_key ON users (lower(username)) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX users_single_admin_key ON users (role) WHERE role = 'admin' AND deleted_at IS NULL;

CREATE TABLE sessions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE,
  user_agent text NOT NULL DEFAULT '',
  ip inet,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  idle_expires_at timestamptz NOT NULL,
  absolute_expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  revoke_reason text
);
CREATE INDEX sessions_user_active_idx ON sessions(user_id, absolute_expires_at) WHERE revoked_at IS NULL;

CREATE TABLE login_events (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  username text NOT NULL,
  success boolean NOT NULL,
  reason text NOT NULL,
  ip inet,
  user_agent text NOT NULL DEFAULT '',
  occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_logs (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  actor_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  action text NOT NULL,
  target_type text NOT NULL,
  target_id text NOT NULL,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  request_id text NOT NULL,
  ip inet,
  occurred_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE audit_logs;
DROP TABLE login_events;
DROP TABLE sessions;
DROP TABLE users;
DROP TYPE user_status;
DROP TYPE user_role;
