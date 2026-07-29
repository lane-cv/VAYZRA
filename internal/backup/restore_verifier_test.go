package backup

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type restoreDatabaseStub struct {
	counts            map[string]int64
	references        []RestoreObjectReference
	activeSessions    int64
	migration         int64
	activeSessionsErr error
	migrationErr      error
	countErr          error
	referencesErr     error
	countedTables     []string
	calls             []string
}

func (stub *restoreDatabaseStub) CountRows(_ context.Context, table string) (int64, error) {
	stub.calls = append(stub.calls, "count:"+table)
	stub.countedTables = append(stub.countedTables, table)
	if stub.countErr != nil {
		return 0, stub.countErr
	}
	count, ok := stub.counts[table]
	if !ok {
		return 0, errors.New("unexpected table")
	}
	return count, nil
}

func (stub *restoreDatabaseStub) ForEachLiveObject(
	_ context.Context,
	visit func(RestoreObjectReference) error,
) error {
	stub.calls = append(stub.calls, "references")
	for _, reference := range stub.references {
		if err := visit(reference); err != nil {
			return err
		}
	}
	return stub.referencesErr
}

func (stub *restoreDatabaseStub) ActiveSessionCount(context.Context) (int64, error) {
	stub.calls = append(stub.calls, "sessions")
	return stub.activeSessions, stub.activeSessionsErr
}

func (stub *restoreDatabaseStub) MigrationVersion(context.Context) (int64, error) {
	stub.calls = append(stub.calls, "migration")
	return stub.migration, stub.migrationErr
}

type restoreObjectsStub struct {
	sizes  map[string]int64
	errors map[string]error
	keys   []string
}

func (stub *restoreObjectsStub) Stat(_ context.Context, key string) (int64, error) {
	stub.keys = append(stub.keys, key)
	if err := stub.errors[key]; err != nil {
		return 0, err
	}
	size, ok := stub.sizes[key]
	if !ok {
		return 0, ErrRestoreObjectNotFound
	}
	return size, nil
}

func TestRestoreVerifierCountsAllowlistAndStatsEveryLiveReference(t *testing.T) {
	counts := make(map[string]int64, len(restoreRowCountAllowlist))
	for index, table := range restoreRowCountAllowlist {
		counts[table] = int64(index + 1)
	}
	database := &restoreDatabaseStub{
		counts: counts,
		references: []RestoreObjectReference{
			{
				Source: RestoreFileVersion, Repository: RestoreOriginals,
				ObjectKey: "original/a", Size: 11,
			},
			{
				Source: RestoreFilePreview, Repository: RestorePreviews,
				ObjectKey: "preview/b", Size: 12,
			},
			{
				Source: RestoreProcessingArtifact, Repository: RestorePreviews,
				ObjectKey: "preview/c", Size: 13,
			},
		},
		migration: 20,
	}
	originals := &restoreObjectsStub{sizes: map[string]int64{"original/a": 11}}
	previews := &restoreObjectsStub{sizes: map[string]int64{
		"preview/b": 12,
		"preview/c": 13,
	}}

	result, err := NewRestoreVerifier(database, originals, previews).
		Verify(context.Background())
	if err != nil {
		t.Fatalf("verify restored data: %v", err)
	}
	if !reflect.DeepEqual(database.countedTables, restoreRowCountAllowlist[:]) {
		t.Fatalf("counted tables=%v", database.countedTables)
	}
	if len(database.calls) == 0 || database.calls[0] != "sessions" {
		t.Fatalf("first database call=%v", database.calls)
	}
	if result.CheckedObjectCount != 3 ||
		result.MissingObjectCount != 0 ||
		result.UnexpectedObjectCount != 0 ||
		!result.SessionRevocationVerified ||
		result.RestoredMigrationVersion != 20 ||
		len(result.ReportSHA256) != 32 {
		t.Fatalf("result=%+v", result)
	}
	if !reflect.DeepEqual(result.DatabaseRowCounts, counts) {
		t.Fatalf("row counts=%v", result.DatabaseRowCounts)
	}
	if !reflect.DeepEqual(originals.keys, []string{"original/a"}) ||
		!reflect.DeepEqual(previews.keys, []string{"preview/b", "preview/c"}) {
		t.Fatalf("stats originals=%v previews=%v", originals.keys, previews.keys)
	}
}

func TestRestoreVerifierFailsClosedForMissingOrWrongSizedObject(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		sizes map[string]int64
	}{
		{name: "missing", sizes: map[string]int64{}},
		{name: "wrong size", sizes: map[string]int64{"original/a": 8}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			database := validRestoreDatabase()
			database.references = []RestoreObjectReference{
				{
					Source: RestoreFileVersion, Repository: RestoreOriginals,
					ObjectKey: "original/a", Size: 9,
				},
			}
			result, err := NewRestoreVerifier(
				database,
				&restoreObjectsStub{sizes: testCase.sizes},
				&restoreObjectsStub{sizes: map[string]int64{}},
			).Verify(context.Background())
			if !errors.Is(err, ErrRestoreObjectIntegrity) {
				t.Fatalf("error=%v", err)
			}
			if result.CheckedObjectCount != 1 {
				t.Fatalf("checked=%d", result.CheckedObjectCount)
			}
			if testCase.name == "missing" && result.MissingObjectCount != 1 {
				t.Fatalf("missing=%d", result.MissingObjectCount)
			}
			if testCase.name == "wrong size" && result.UnexpectedObjectCount != 1 {
				t.Fatalf("unexpected=%d", result.UnexpectedObjectCount)
			}
		})
	}
}

func TestRestoreVerifierRejectsStaleSessionBeforeObjectAccess(t *testing.T) {
	database := validRestoreDatabase()
	database.activeSessions = 1
	originals := &restoreObjectsStub{sizes: map[string]int64{}}
	_, err := NewRestoreVerifier(
		database,
		originals,
		&restoreObjectsStub{sizes: map[string]int64{}},
	).Verify(context.Background())
	if !errors.Is(err, ErrRestoreSessionRevocation) {
		t.Fatalf("error=%v", err)
	}
	if len(originals.keys) != 0 {
		t.Fatalf("object access occurred before session proof: %v", originals.keys)
	}
	if !reflect.DeepEqual(database.calls, []string{"sessions"}) {
		t.Fatalf("database access occurred before session proof: %v", database.calls)
	}
}

func TestRestoreVerifierRejectsUnknownDuplicateAndMisroutedReferences(t *testing.T) {
	for _, testCase := range []struct {
		name              string
		references        []RestoreObjectReference
		wantChecked       int64
		wantOriginalStats []string
	}{
		{
			name: "unknown repository",
			references: []RestoreObjectReference{{
				Source: RestoreFileVersion, Repository: RestoreRepository("unknown"),
				ObjectKey: "secret/unknown-repository", Size: 9,
			}},
		},
		{
			name: "duplicate",
			references: []RestoreObjectReference{
				{
					Source: RestoreFileVersion, Repository: RestoreOriginals,
					ObjectKey: "secret/duplicate", Size: 9,
				},
				{
					Source: RestoreFileVersion, Repository: RestoreOriginals,
					ObjectKey: "secret/duplicate", Size: 9,
				},
			},
			wantChecked:       1,
			wantOriginalStats: []string{"secret/duplicate"},
		},
		{
			name: "unexpected authorization route",
			references: []RestoreObjectReference{{
				Source: RestoreFileVersion, Repository: RestorePreviews,
				ObjectKey: "secret/misrouted", Size: 9,
			}},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			database := validRestoreDatabase()
			database.references = testCase.references
			originals := &restoreObjectsStub{
				sizes: map[string]int64{"secret/duplicate": 9},
			}
			result, err := NewRestoreVerifier(
				database,
				originals,
				&restoreObjectsStub{sizes: map[string]int64{}},
			).Verify(context.Background())
			if !errors.Is(err, ErrRestoreObjectIntegrity) {
				t.Fatalf("error=%v", err)
			}
			if result.CheckedObjectCount != testCase.wantChecked ||
				result.UnexpectedObjectCount != 1 {
				t.Fatalf("result=%+v", result)
			}
			if !reflect.DeepEqual(originals.keys, testCase.wantOriginalStats) {
				t.Fatalf("original stats=%v", originals.keys)
			}
		})
	}
}

func TestRestoreVerifierReportIsDeterministicAndDoesNotContainObjectKeys(t *testing.T) {
	verify := func(key string, size int64) RestoreVerificationResult {
		t.Helper()
		database := validRestoreDatabase()
		database.references = []RestoreObjectReference{{
			Source: RestoreFileVersion, Repository: RestoreOriginals,
			ObjectKey: key, Size: size,
		}}
		result, err := NewRestoreVerifier(
			database,
			&restoreObjectsStub{sizes: map[string]int64{key: size}},
			&restoreObjectsStub{sizes: map[string]int64{}},
		).Verify(context.Background())
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		return result
	}

	first := verify("secret/first-object-key", 9)
	second := verify("secret/second-object-key", 19)
	if !reflect.DeepEqual(first.ReportSHA256, second.ReportSHA256) {
		t.Fatalf("report hashes differ: %x != %x", first.ReportSHA256, second.ReportSHA256)
	}
	rendered := fmt.Sprintf("%+v", first)
	for _, secret := range []string{"secret/first-object-key", "credential"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("result leaked %q: %s", secret, rendered)
		}
	}
}

func TestRestoreVerifierPropagatesContextAndDependencyErrorsWithoutLeakingDetails(t *testing.T) {
	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		database := validRestoreDatabase()
		_, err := NewRestoreVerifier(
			database,
			&restoreObjectsStub{sizes: map[string]int64{}},
			&restoreObjectsStub{sizes: map[string]int64{}},
		).Verify(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
		if len(database.calls) != 0 {
			t.Fatalf("database calls=%v", database.calls)
		}
	})

	for _, testCase := range []struct {
		name  string
		setup func(*restoreDatabaseStub, *restoreObjectsStub) error
	}{
		{
			name: "database",
			setup: func(database *restoreDatabaseStub, _ *restoreObjectsStub) error {
				err := errors.New("database credential=secret")
				database.activeSessionsErr = err
				return err
			},
		},
		{
			name: "object store",
			setup: func(database *restoreDatabaseStub, originals *restoreObjectsStub) error {
				err := errors.New("object credential=secret")
				database.references = []RestoreObjectReference{{
					Source: RestoreFileVersion, Repository: RestoreOriginals,
					ObjectKey: "secret/dependency-key", Size: 9,
				}}
				originals.errors = map[string]error{"secret/dependency-key": err}
				return err
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			database := validRestoreDatabase()
			originals := &restoreObjectsStub{sizes: map[string]int64{}}
			dependencyErr := testCase.setup(database, originals)
			_, err := NewRestoreVerifier(
				database,
				originals,
				&restoreObjectsStub{sizes: map[string]int64{}},
			).Verify(context.Background())
			if !errors.Is(err, dependencyErr) {
				t.Fatalf("error=%v", err)
			}
			for _, secret := range []string{"credential=secret", "secret/dependency-key"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestRestoreVerifierRejectsUnsafeReportIntegers(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		setup func(*restoreDatabaseStub)
	}{
		{
			name: "negative row count",
			setup: func(database *restoreDatabaseStub) {
				database.counts[restoreRowCountAllowlist[0]] = -1
			},
		},
		{
			name: "nonpositive migration",
			setup: func(database *restoreDatabaseStub) {
				database.migration = 0
			},
		},
		{
			name: "nonpositive object size",
			setup: func(database *restoreDatabaseStub) {
				database.references = []RestoreObjectReference{{
					Source: RestoreFileVersion, Repository: RestoreOriginals,
					ObjectKey: "secret/zero-size", Size: 0,
				}}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			database := validRestoreDatabase()
			testCase.setup(database)
			originals := &restoreObjectsStub{sizes: map[string]int64{}}
			result, err := NewRestoreVerifier(
				database,
				originals,
				&restoreObjectsStub{sizes: map[string]int64{}},
			).Verify(context.Background())
			if !errors.Is(err, ErrRestoreUnsafeReportValue) {
				t.Fatalf("error=%v", err)
			}
			if len(originals.keys) != 0 {
				t.Fatalf("stats=%v result=%+v", originals.keys, result)
			}
		})
	}
}

func TestRestoreVerifierUsesExactRowCountAllowlist(t *testing.T) {
	want := []string{
		"users", "sessions",
		"subjects", "grades", "terms", "chapters",
		"lessons", "lesson_revisions",
		"files", "file_versions", "file_previews",
		"qa_threads", "qa_messages",
		"ai_threads", "ai_messages", "ai_runs",
	}
	if !reflect.DeepEqual(restoreRowCountAllowlist[:], want) {
		t.Fatalf("allowlist=%v", restoreRowCountAllowlist)
	}
}

func validRestoreDatabase() *restoreDatabaseStub {
	counts := make(map[string]int64, len(restoreRowCountAllowlist))
	for _, table := range restoreRowCountAllowlist {
		counts[table] = 0
	}
	return &restoreDatabaseStub{counts: counts, migration: 20}
}
