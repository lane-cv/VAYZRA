package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"happylearn.local/app/internal/backup"
)

func TestRunProgramDispatchesRestoreRecordBeforeBackupConstruction(t *testing.T) {
	var backupConstructions, recordRuns int
	factories := programFactories{
		newActions: func(
			context.Context,
			func(string) string,
		) (commandActions, func(), error) {
			backupConstructions++
			return nil, func() {}, errors.New("must not construct backup actions")
		},
		runRestoreRecord: func(
			context.Context,
			func(string) string,
		) error {
			recordRuns++
			return nil
		},
	}
	if err := runProgram(
		context.Background(),
		[]string{"restore-record"},
		func(string) string { return "" },
		factories,
	); err != nil {
		t.Fatal(err)
	}
	if recordRuns != 1 || backupConstructions != 0 {
		t.Fatalf(
			"recordRuns=%d backupConstructions=%d",
			recordRuns,
			backupConstructions,
		)
	}
	for _, args := range [][]string{
		{"restore-record", "--report-file", "/tmp/report"},
		{"restore-record", ""},
	} {
		if err := runProgram(
			context.Background(),
			args,
			func(string) string { return "" },
			factories,
		); !errors.Is(err, errInvalidCommand) {
			t.Fatalf("args=%q err=%v", args, err)
		}
	}
}

func TestRecordRestoreExerciseMapsCanonicalReportToSuccessInput(t *testing.T) {
	store := &restoreRecordStoreStub{}
	finishedAt := time.Date(2026, 7, 30, 2, 30, 0, 0, time.UTC)
	if err := recordRestoreExercise(
		context.Background(),
		strings.NewReader(fixedCommandRestoreExerciseReport()),
		store,
		func() time.Time { return finishedAt },
	); err != nil {
		t.Fatal(err)
	}
	input := store.input
	if store.calls != 1 ||
		input.VerificationID.String() !=
			"22222222-2222-4222-8222-222222222222" ||
		input.BackupRunID.String() !=
			"11111111-1111-4111-8111-111111111111" ||
		!input.StartedAt.Equal(finishedAt.Add(-54*time.Second)) ||
		!input.FinishedAt.Equal(finishedAt) ||
		input.RestoredMigrationVersion != 22 ||
		input.DatabaseRowCounts["users"] != 1 ||
		input.DatabaseRowCounts["ai_runs"] != 16 ||
		input.CheckedObjectCount != 2 ||
		!input.SessionRevocationVerified ||
		input.RTOSeconds != 54 ||
		len(input.ManifestSHA256) != sha256.Size ||
		len(input.ReportSHA256) != sha256.Size {
		t.Fatalf("calls=%d input=%+v", store.calls, input)
	}

	if err := recordRestoreExercise(
		context.Background(),
		strings.NewReader(`{"schemaVersion":2}`+"\n"),
		store,
		time.Now,
	); !errors.Is(err, errWorkflowUnavailable) {
		t.Fatalf("invalid report err=%v", err)
	}
	if store.calls != 1 {
		t.Fatalf("invalid report reached store calls=%d", store.calls)
	}
}

func TestRestoreRecordDatabaseURLHasBoundedConnectionAndStatements(t *testing.T) {
	databaseURL, err := restoreRecordDatabaseURL(
		restoreRecordDatabaseConfig{
			host: "postgres", port: "5432", user: "happylearn",
			name: "happylearn", sslMode: "require",
		},
		"database password",
	)
	if err != nil {
		t.Fatal(err)
	}
	password, ok := databaseURL.User.Password()
	query := databaseURL.Query()
	if databaseURL.Host != "postgres:5432" ||
		strings.TrimPrefix(databaseURL.Path, "/") != "happylearn" ||
		databaseURL.User.Username() != "happylearn" ||
		!ok ||
		password != "database password" ||
		query.Get("sslmode") != "require" ||
		query.Get("connect_timeout") != "5" ||
		query.Get("statement_timeout") != "30000" ||
		query.Get("lock_timeout") != "10000" {
		t.Fatalf("url=%s query=%v", databaseURL.Redacted(), query)
	}
}

type restoreRecordStoreStub struct {
	calls int
	input backup.RestoreSuccessInput
	err   error
}

func (store *restoreRecordStoreStub) RecordRestoreSuccess(
	_ context.Context,
	input backup.RestoreSuccessInput,
) (backup.RestoreVerification, error) {
	store.calls++
	store.input = input
	return backup.RestoreVerification{
		ID:          input.VerificationID,
		BackupRunID: input.BackupRunID,
		State:       backup.RestoreSucceeded,
	}, store.err
}

func fixedCommandRestoreExerciseReport() string {
	counts := `{"users":1,"sessions":2,"subjects":3,"grades":4,` +
		`"terms":5,"chapters":6,"lessons":7,"lesson_revisions":8,` +
		`"files":9,"file_versions":10,"file_previews":11,` +
		`"qa_threads":12,"qa_messages":13,"ai_threads":14,` +
		`"ai_messages":15,"ai_runs":16}`
	verificationID := "22222222-2222-4222-8222-222222222222"
	backupID := "11111111-1111-4111-8111-111111111111"
	manifest := strings.Repeat("a", 64)
	verification := strings.Repeat("b", 64)
	evidence := strings.Repeat("c", 64)
	canonical := fmt.Sprintf(
		"schemaVersion=2\n"+
			"verificationId=%s\nbackupId=%s\nmanifestSHA256=%s\n"+
			"verificationReportSHA256=%s\nevidenceSHA256=%s\n"+
			"durationSeconds=54\nmigrationVersion=22\n"+
			"databaseRowCounts=%s\nrowCountTotal=136\n"+
			"checkedObjectCount=2\nmissingObjectCount=0\n"+
			"unexpectedObjectCount=0\nactiveSessionCount=0\n"+
			"isolation404ProbeCount=2\n",
		verificationID,
		backupID,
		manifest,
		verification,
		evidence,
		counts,
	)
	reportSHA256 := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf(
		`{"schemaVersion":2,"verificationId":"%s","backupId":"%s",`+
			`"manifestSHA256":"%s","verificationReportSHA256":"%s",`+
			`"evidenceSHA256":"%s","durationSeconds":54,`+
			`"migrationVersion":22,"databaseRowCounts":%s,`+
			`"rowCountTotal":136,"checkedObjectCount":2,`+
			`"missingObjectCount":0,"unexpectedObjectCount":0,`+
			`"activeSessionCount":0,"isolation404ProbeCount":2,`+
			`"reportSHA256":"%s"}`+"\n",
		verificationID,
		backupID,
		manifest,
		verification,
		evidence,
		counts,
		hex.EncodeToString(reportSHA256[:]),
	)
}

var _ restoreSuccessRecorder = (*restoreRecordStoreStub)(nil)
