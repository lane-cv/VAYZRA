package audit

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/tests/integration"
)

func TestBackupRequestedAuditEventHasExactSafeShape(t *testing.T) {
	pool := integration.StartPostgres(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE audit_logs,users CASCADE`); err != nil {
		t.Fatal(err)
	}
	actorID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO users(id,username,display_name,role,password_hash,must_change_password)
VALUES($1,'backup_audit_admin','Backup Audit Admin','admin','hash',false)`,
		actorID,
	); err != nil {
		t.Fatal(err)
	}
	event := Event{
		ActorUserID: actorID,
		Action:      "operations.backup_requested",
		TargetType:  "backup_run",
		TargetID:    uuid.NewString(),
		Metadata:    map[string]any{},
		RequestID:   "backup-request-audit",
		IP:          net.ParseIP("192.0.2.40"),
	}
	writer := NewPostgresWriter(pool)
	if err := writer.Write(ctx, event); err != nil {
		t.Fatalf("write safe backup audit: %v", err)
	}
	for _, mutate := range []func(*Event){
		func(value *Event) { value.TargetID = "/private/repository" },
		func(value *Event) { value.Metadata = map[string]any{"path": "/private/repository"} },
	} {
		invalid := event
		mutate(&invalid)
		if err := writer.Write(ctx, invalid); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("unsafe event err=%v", err)
		}
	}
}
