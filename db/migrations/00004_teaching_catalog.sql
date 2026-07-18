-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE grades (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 80),
  name_norm text GENERATED ALWAYS AS (lower(btrim(name))) STORED,
  sort_key bigint NOT NULL DEFAULT 1024,
  archived_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (name_norm)
);

CREATE TABLE terms (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  grade_id uuid NOT NULL REFERENCES grades(id) ON DELETE RESTRICT,
  name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 80),
  name_norm text GENERATED ALWAYS AS (lower(btrim(name))) STORED,
  sort_key bigint NOT NULL DEFAULT 1024,
  archived_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (grade_id, name_norm)
);

CREATE TABLE subjects (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  term_id uuid NOT NULL REFERENCES terms(id) ON DELETE RESTRICT,
  name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 80),
  name_norm text GENERATED ALWAYS AS (lower(btrim(name))) STORED,
  sort_key bigint NOT NULL DEFAULT 1024,
  archived_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (term_id, name_norm)
);

CREATE TABLE chapters (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  subject_id uuid NOT NULL REFERENCES subjects(id) ON DELETE RESTRICT,
  name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 80),
  name_norm text GENERATED ALWAYS AS (lower(btrim(name))) STORED,
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
  sort_key bigint NOT NULL DEFAULT 1024,
  archived_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (subject_id, name_norm)
);

CREATE TABLE lessons (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  chapter_id uuid NOT NULL REFERENCES chapters(id) ON DELETE RESTRICT,
  published_revision_id uuid,
  archived_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE lesson_drafts (
  lesson_id uuid PRIMARY KEY REFERENCES lessons(id) ON DELETE CASCADE,
  title text NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 160),
  summary text NOT NULL DEFAULT '' CHECK (char_length(summary) <= 500),
  body_markdown text NOT NULL DEFAULT '' CHECK (char_length(body_markdown) <= 200000),
  sort_key bigint NOT NULL DEFAULT 1024,
  lock_version bigint NOT NULL DEFAULT 1 CHECK (lock_version > 0),
  updated_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE lesson_revisions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  lesson_id uuid NOT NULL REFERENCES lessons(id) ON DELETE RESTRICT,
  version bigint NOT NULL CHECK (version > 0),
  title text NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 160),
  summary text NOT NULL DEFAULT '' CHECK (char_length(summary) <= 500),
  body_markdown text NOT NULL DEFAULT '' CHECK (char_length(body_markdown) <= 200000),
  sort_key bigint NOT NULL DEFAULT 1024,
  published_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  published_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (lesson_id, version)
);

ALTER TABLE lessons ADD CONSTRAINT lessons_published_revision_id_fkey
  FOREIGN KEY (published_revision_id) REFERENCES lesson_revisions(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE lesson_draft_audiences (
  lesson_id uuid PRIMARY KEY REFERENCES lesson_drafts(lesson_id) ON DELETE CASCADE,
  mode text NOT NULL CHECK (mode IN ('all', 'selected'))
);

CREATE TABLE lesson_draft_audience_users (
  lesson_id uuid NOT NULL REFERENCES lesson_draft_audiences(lesson_id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  PRIMARY KEY (lesson_id, user_id)
);

CREATE TABLE lesson_revision_audiences (
  revision_id uuid PRIMARY KEY REFERENCES lesson_revisions(id) ON DELETE RESTRICT,
  mode text NOT NULL CHECK (mode IN ('all', 'selected'))
);

CREATE TABLE lesson_revision_audience_users (
  revision_id uuid NOT NULL REFERENCES lesson_revision_audiences(revision_id) ON DELETE RESTRICT,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  PRIMARY KEY (revision_id, user_id)
);

CREATE TABLE lesson_draft_external_videos (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  lesson_id uuid NOT NULL REFERENCES lesson_drafts(lesson_id) ON DELETE CASCADE,
  url text NOT NULL CHECK (url ~ '^https://[^/?#[:space:]]+'),
  title text NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 160),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
  sort_key bigint NOT NULL DEFAULT 1024
);

CREATE TABLE lesson_revision_external_videos (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  revision_id uuid NOT NULL REFERENCES lesson_revisions(id) ON DELETE RESTRICT,
  url text NOT NULL CHECK (url ~ '^https://[^/?#[:space:]]+'),
  title text NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 160),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
  sort_key bigint NOT NULL DEFAULT 1024
);

CREATE TABLE outbox_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind text NOT NULL CHECK (char_length(btrim(kind)) BETWEEN 1 AND 160),
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz
);

CREATE TABLE lesson_progress (
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  revision_id uuid NOT NULL REFERENCES lesson_revisions(id) ON DELETE RESTRICT,
  viewed boolean NOT NULL DEFAULT false,
  anchor text NOT NULL DEFAULT '' CHECK (char_length(anchor) <= 160),
  scroll_ratio double precision NOT NULL DEFAULT 0 CHECK (scroll_ratio >= 0 AND scroll_ratio <= 1),
  observed_at timestamptz NOT NULL DEFAULT now(),
  first_viewed_at timestamptz NOT NULL DEFAULT now(),
  last_viewed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, revision_id)
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION require_student_audience_member() RETURNS trigger AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM users WHERE id = NEW.user_id AND role = 'student') THEN
    RAISE EXCEPTION 'lesson audience users must be students' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER lesson_draft_audience_users_student_only
  BEFORE INSERT OR UPDATE ON lesson_draft_audience_users
  FOR EACH ROW EXECUTE FUNCTION require_student_audience_member();
CREATE TRIGGER lesson_revision_audience_users_student_only
  BEFORE INSERT OR UPDATE ON lesson_revision_audience_users
  FOR EACH ROW EXECUTE FUNCTION require_student_audience_member();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reject_lesson_revision_mutation() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'lesson revisions are immutable' USING ERRCODE = '55000';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER lesson_revisions_immutable
  BEFORE UPDATE OR DELETE ON lesson_revisions
  FOR EACH ROW EXECUTE FUNCTION reject_lesson_revision_mutation();
CREATE TRIGGER lesson_revision_audiences_immutable
  BEFORE UPDATE OR DELETE ON lesson_revision_audiences
  FOR EACH ROW EXECUTE FUNCTION reject_lesson_revision_mutation();
CREATE TRIGGER lesson_revision_audience_users_immutable
  BEFORE UPDATE OR DELETE ON lesson_revision_audience_users
  FOR EACH ROW EXECUTE FUNCTION reject_lesson_revision_mutation();
CREATE TRIGGER lesson_revision_external_videos_immutable
  BEFORE UPDATE OR DELETE ON lesson_revision_external_videos
  FOR EACH ROW EXECUTE FUNCTION reject_lesson_revision_mutation();

CREATE INDEX grades_active_sort_idx ON grades (sort_key, id) WHERE archived_at IS NULL;
CREATE INDEX terms_active_tree_idx ON terms (grade_id, sort_key, id) WHERE archived_at IS NULL;
CREATE INDEX subjects_active_tree_idx ON subjects (term_id, sort_key, id) WHERE archived_at IS NULL;
CREATE INDEX chapters_active_tree_idx ON chapters (subject_id, sort_key, id) WHERE archived_at IS NULL;
CREATE INDEX lessons_active_tree_idx ON lessons (chapter_id, id) WHERE archived_at IS NULL;
CREATE INDEX lessons_published_revision_id_idx ON lessons (published_revision_id) WHERE published_revision_id IS NOT NULL;
CREATE INDEX lesson_revisions_title_trgm_idx ON lesson_revisions USING gin (title gin_trgm_ops);
CREATE INDEX lesson_revisions_body_markdown_trgm_idx ON lesson_revisions USING gin (body_markdown gin_trgm_ops);
CREATE INDEX outbox_events_unpublished_idx ON outbox_events (created_at, id) WHERE published_at IS NULL;
CREATE INDEX lesson_progress_recent_idx ON lesson_progress (user_id, last_viewed_at DESC);

-- +goose Down
DROP INDEX lesson_progress_recent_idx;
DROP INDEX outbox_events_unpublished_idx;
DROP INDEX lesson_revisions_body_markdown_trgm_idx;
DROP INDEX lesson_revisions_title_trgm_idx;
DROP INDEX lessons_published_revision_id_idx;
DROP INDEX lessons_active_tree_idx;
DROP INDEX chapters_active_tree_idx;
DROP INDEX subjects_active_tree_idx;
DROP INDEX terms_active_tree_idx;
DROP INDEX grades_active_sort_idx;
DROP TRIGGER lesson_revision_external_videos_immutable ON lesson_revision_external_videos;
DROP TRIGGER lesson_revision_audience_users_immutable ON lesson_revision_audience_users;
DROP TRIGGER lesson_revision_audiences_immutable ON lesson_revision_audiences;
DROP TRIGGER lesson_revisions_immutable ON lesson_revisions;
DROP FUNCTION reject_lesson_revision_mutation();
DROP TRIGGER lesson_revision_audience_users_student_only ON lesson_revision_audience_users;
DROP TRIGGER lesson_draft_audience_users_student_only ON lesson_draft_audience_users;
DROP FUNCTION require_student_audience_member();
ALTER TABLE lessons DROP CONSTRAINT lessons_published_revision_id_fkey;
DROP TABLE lesson_progress;
DROP TABLE outbox_events;
DROP TABLE lesson_revision_external_videos;
DROP TABLE lesson_draft_external_videos;
DROP TABLE lesson_revision_audience_users;
DROP TABLE lesson_revision_audiences;
DROP TABLE lesson_draft_audience_users;
DROP TABLE lesson_draft_audiences;
DROP TABLE lesson_revisions;
DROP TABLE lesson_drafts;
DROP TABLE lessons;
DROP TABLE chapters;
DROP TABLE subjects;
DROP TABLE terms;
DROP TABLE grades;
