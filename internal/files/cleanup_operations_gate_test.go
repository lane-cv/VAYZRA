package files

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/operations"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/objectstore"
	"happylearn.local/app/tests/integration"
)

const cleanupOperationsAdvisoryKey = 845103120

type cleanupMaintenanceResult struct {
	lease operations.Lease
	err   error
}

type blockingCleanupObjects struct {
	*fakeObjects
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (o *blockingCleanupObjects) AbortMultipart(
	ctx context.Context,
	key string,
	uploadID string,
) error {
	o.once.Do(func() { close(o.entered) })
	select {
	case <-o.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return o.fakeObjects.AbortMultipart(ctx, key, uploadID)
}

type admissionBlockingCleaner struct {
	delegate ExpiredUploadCleaner
	entered  chan struct{}
	proceed  chan struct{}
	calls    atomic.Int32
	once     sync.Once
}

func (c *admissionBlockingCleaner) CleanupExpired(
	ctx context.Context,
	limit int,
) error {
	c.calls.Add(1)
	c.once.Do(func() { close(c.entered) })
	select {
	case <-c.proceed:
	case <-ctx.Done():
		return ctx.Err()
	}
	return c.delegate.CleanupExpired(ctx, limit)
}

type observedCleanupCleaner struct {
	delegate ExpiredUploadCleaner
	calls    atomic.Int32
}

func (c *observedCleanupCleaner) CleanupExpired(
	ctx context.Context,
	limit int,
) error {
	c.calls.Add(1)
	return c.delegate.CleanupExpired(ctx, limit)
}

func TestPostgresCleanupRunnerHoldsMaintenanceGateThroughSettlement(t *testing.T) {
	objects := &blockingCleanupObjects{
		fakeObjects: newFakeObjects(),
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	pool, gate, service, session := postgresCleanupRunnerFixture(t, objects)
	runner := newCleanupRunner(
		service,
		gate,
		nil,
		time.Hour,
		5*time.Second,
		100,
	)
	runDone := make(chan struct{})
	go func() {
		runner.runOnce(context.Background())
		close(runDone)
	}()
	select {
	case <-objects.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not reach object settlement")
	}

	maintenance := startCleanupMaintenance(t, pool, gate)
	select {
	case result := <-maintenance:
		close(objects.release)
		if result.err == nil {
			_ = gate.ReleaseLease(context.Background(), result.lease)
		}
		<-runDone
		t.Fatalf("maintenance entered before settlement completed: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := waitForQueuedAdvisoryLock(pool); err != nil {
		close(objects.release)
		<-runDone
		t.Fatal(err)
	}
	var mode string
	if err := pool.QueryRow(
		context.Background(),
		`SELECT mode FROM operational_modes WHERE singleton_id=true`,
	).Scan(&mode); err != nil {
		close(objects.release)
		<-runDone
		t.Fatal(err)
	}
	if mode != "normal" {
		close(objects.release)
		<-runDone
		t.Fatalf("mode=%q before settlement release", mode)
	}

	close(objects.release)
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not finish after object settlement")
	}
	result := <-maintenance
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer gate.ReleaseLease(context.Background(), result.lease)
	var sessions int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM upload_sessions WHERE id=$1`,
		session.ID,
	).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 ||
		objects.abortCalls.Load() != 1 ||
		objects.deleteCalls.Load() != 1 {
		t.Fatalf(
			"sessions=%d aborts=%d deletes=%d",
			sessions,
			objects.abortCalls.Load(),
			objects.deleteCalls.Load(),
		)
	}
}

func TestPostgresCleanupRunnerAvoidsDoubleAdmissionBehindQueuedMaintenance(
	t *testing.T,
) {
	objects := newFakeObjects()
	pool, gate, service, session := postgresCleanupRunnerFixture(t, objects)
	cleaner := &admissionBlockingCleaner{
		delegate: service,
		entered:  make(chan struct{}),
		proceed:  make(chan struct{}),
	}
	runner := newCleanupRunner(
		cleaner,
		gate,
		nil,
		time.Hour,
		5*time.Second,
		100,
	)
	runDone := make(chan struct{})
	go func() {
		runner.runOnce(context.Background())
		close(runDone)
	}()
	select {
	case <-cleaner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not pass outer admission")
	}

	maintenance := startCleanupMaintenance(t, pool, gate)
	select {
	case result := <-maintenance:
		close(cleaner.proceed)
		if result.err == nil {
			_ = gate.ReleaseLease(context.Background(), result.lease)
		}
		<-runDone
		t.Fatalf("maintenance entered before admitted cleanup ran: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := waitForQueuedAdvisoryLock(pool); err != nil {
		close(cleaner.proceed)
		<-runDone
		t.Fatal(err)
	}

	close(cleaner.proceed)
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup self-blocked behind queued maintenance")
	}
	result := <-maintenance
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer gate.ReleaseLease(context.Background(), result.lease)
	var sessions int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM upload_sessions WHERE id=$1`,
		session.ID,
	).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if cleaner.calls.Load() != 1 || sessions != 0 ||
		objects.abortCalls.Load() != 1 ||
		objects.deleteCalls.Load() != 1 {
		t.Fatalf(
			"calls=%d sessions=%d aborts=%d deletes=%d",
			cleaner.calls.Load(),
			sessions,
			objects.abortCalls.Load(),
			objects.deleteCalls.Load(),
		)
	}
}

func TestPostgresCleanupRunnerRejectsMaintenanceWithoutMutation(t *testing.T) {
	for _, maintenance := range []string{"active", "queued"} {
		t.Run(maintenance, func(t *testing.T) {
			objects := newFakeObjects()
			pool, gate, service, session := postgresCleanupRunnerFixture(
				t,
				objects,
			)
			cleaner := &observedCleanupCleaner{delegate: service}
			var categories []string
			runner := newCleanupRunner(
				cleaner,
				gate,
				func(category string) {
					categories = append(categories, category)
				},
				time.Hour,
				time.Second,
				100,
			)
			var releaseMaintenance func()
			switch maintenance {
			case "active":
				result := <-startCleanupMaintenance(t, pool, gate)
				if result.err != nil {
					t.Fatal(result.err)
				}
				releaseMaintenance = func() {
					if err := gate.ReleaseLease(
						context.Background(),
						result.lease,
					); err != nil {
						t.Fatal(err)
					}
				}
			case "queued":
				releaseMaintenance =
					integration.QueueOperationsExclusiveBehindShared(t, pool)
			default:
				t.Fatal("unknown maintenance case")
			}

			started := time.Now()
			runner.runOnce(context.Background())
			elapsed := time.Since(started)
			releaseMaintenance()
			if elapsed > time.Second {
				t.Fatalf("maintenance rejection took %v", elapsed)
			}
			var state string
			if err := pool.QueryRow(
				context.Background(),
				`SELECT state FROM upload_sessions WHERE id=$1`,
				session.ID,
			).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if cleaner.calls.Load() != 0 ||
				strings.Join(categories, ",") != "operational_gate_failed" ||
				state != string(UploadOpen) ||
				objects.abortCalls.Load() != 0 ||
				objects.deleteCalls.Load() != 0 {
				t.Fatalf(
					"calls=%d log=%q state=%q aborts=%d deletes=%d",
					cleaner.calls.Load(),
					strings.Join(categories, ","),
					state,
					objects.abortCalls.Load(),
					objects.deleteCalls.Load(),
				)
			}
		})
	}
}

func postgresCleanupRunnerFixture(
	t *testing.T,
	objects objectstore.Store,
) (*pgxpool.Pool, *operations.PostgresStore, *UploadService, UploadSession) {
	t.Helper()
	ctx := context.Background()
	pool := integration.StartPostgres(t)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
DELETE FROM upload_parts;
DELETE FROM upload_sessions;
UPDATE operational_modes
SET mode='normal',owner_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,
    entered_at=NULL,updated_at=clock_timestamp(),version=version+1
WHERE singleton_id=true`); err != nil {
		t.Fatal(err)
	}
	gate := operations.NewPostgresStore(pool)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := gate.Close(closeCtx); err != nil {
			t.Errorf("close operations gate: %v", err)
		}
		tag, err := pool.Exec(closeCtx, `
UPDATE operational_modes
SET mode='normal',owner_id=NULL,lease_token_hash=NULL,lease_expires_at=NULL,
    entered_at=NULL,updated_at=clock_timestamp(),version=version+1
WHERE singleton_id=true`)
		if err != nil {
			t.Errorf("restore operational mode: %v", err)
		} else if tag.RowsAffected() != 1 {
			t.Errorf("restore operational mode rows=%d", tag.RowsAffected())
		}
	})
	actorID := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO users(id,username,display_name,role,status,password_hash)
VALUES($1,$2,'Cleanup gate','student','active','hash')`,
		actorID, "cleanup_gate_"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	session := UploadSession{
		ID:             uuid.New(),
		ActorUserID:    actorID,
		Purpose:        UploadPurposeTeaching,
		ObjectKey:      "cleanup-gate/" + uuid.NewString(),
		MinIOUploadID:  uuid.NewString(),
		DisplayName:    "cleanup.pdf",
		DeclaredMIME:   "application/pdf",
		ExpectedSize:   1,
		ExpectedSHA256: strings.Repeat("a", 64),
		State:          UploadOpen,
		ExpiresAt:      now.Add(-2 * cleanupGrace),
		CreatedAt:      now.Add(-26 * time.Hour),
	}
	store := NewPostgresStore(pool)
	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := pool.Exec(
			cleanupCtx,
			`DELETE FROM upload_parts WHERE upload_session_id=$1`,
			session.ID,
		); err != nil {
			t.Errorf("remove cleanup gate parts: %v", err)
		}
		if _, err := pool.Exec(
			cleanupCtx,
			`DELETE FROM upload_sessions WHERE id=$1`,
			session.ID,
		); err != nil {
			t.Errorf("remove cleanup gate fixture: %v", err)
		}
		if _, err := pool.Exec(
			cleanupCtx,
			`DELETE FROM users WHERE id=$1`,
			actorID,
		); err != nil {
			t.Errorf("remove cleanup gate actor: %v", err)
		}
	})
	service := NewUploadService(
		store,
		objects,
		TeachingUploadPolicy{},
		func() time.Time { return now },
	)
	return pool, gate, service, session
}

func startCleanupMaintenance(
	t *testing.T,
	pool *pgxpool.Pool,
	gate *operations.PostgresStore,
) <-chan cleanupMaintenanceResult {
	t.Helper()
	var now time.Time
	if err := pool.QueryRow(
		context.Background(),
		`SELECT clock_timestamp()`,
	).Scan(&now); err != nil {
		t.Fatal(err)
	}
	result := make(chan cleanupMaintenanceResult, 1)
	go func() {
		lease, err := gate.AcquireLease(
			context.Background(),
			operations.LeaseRequest{
				Mode:      "draining",
				OwnerID:   uuid.New(),
				ExpiresAt: now.Add(time.Minute),
			},
		)
		result <- cleanupMaintenanceResult{lease: lease, err: err}
	}()
	return result
}

func waitForQueuedAdvisoryLock(pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `
SELECT count(*) FROM pg_locks
WHERE locktype='advisory' AND NOT granted
  AND mode='ExclusiveLock'
  AND classid=0
  AND objid=$1::oid
  AND objsubid=1`, cleanupOperationsAdvisoryKey).Scan(&waiting); err != nil {
			return err
		}
		if waiting > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("exclusive maintenance did not queue")
		case <-ticker.C:
		}
	}
}
