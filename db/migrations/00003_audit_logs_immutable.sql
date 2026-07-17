-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reject_audit_log_mutation() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'audit logs are immutable' USING ERRCODE = '55000';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
CREATE TRIGGER audit_logs_immutable BEFORE UPDATE OR DELETE ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_audit_log_mutation();

-- +goose Down
DROP TRIGGER audit_logs_immutable ON audit_logs;
DROP FUNCTION reject_audit_log_mutation();
