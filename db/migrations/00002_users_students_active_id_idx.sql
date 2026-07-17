-- +goose Up
CREATE INDEX users_students_active_id_idx
  ON users (id)
  WHERE role = 'student' AND deleted_at IS NULL;

-- +goose Down
DROP INDEX users_students_active_id_idx;
