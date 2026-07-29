package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestWriteRestoreVerificationReportUsesBoundedFixedOrderAndMode(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "restore-check.report")
	result := fixedRestoreReportResult()

	if err := WriteRestoreVerificationReport(path, result); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	expectedText := "schema_version=1\nmigration_version=20\n" +
		"table_users_count=1\n" +
		"table_sessions_count=2\n" +
		"table_subjects_count=3\n" +
		"table_grades_count=4\n" +
		"table_terms_count=5\n" +
		"table_chapters_count=6\n" +
		"table_lessons_count=7\n" +
		"table_lesson_revisions_count=8\n" +
		"table_files_count=9\n" +
		"table_file_versions_count=10\n" +
		"table_file_previews_count=11\n" +
		"table_qa_threads_count=12\n" +
		"table_qa_messages_count=13\n" +
		"table_ai_threads_count=14\n" +
		"table_ai_messages_count=15\n" +
		"table_ai_runs_count=16\n" +
		"row_count_total=136\n" +
		"checked_object_count=7\n" +
		"missing_object_count=0\n" +
		"unexpected_object_count=0\n" +
		"active_session_count=0\n" +
		"verification_report_sha256=" + hex.EncodeToString(result.ReportSHA256) + "\n"
	if string(content) != expectedText {
		t.Fatalf("report=%q want=%q", content, expectedText)
	}
	if len(content) > maxRestoreReportBytes {
		t.Fatalf("report bytes=%d limit=%d", len(content), maxRestoreReportBytes)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("report mode=%v", info.Mode())
	}
}

func TestWriteBoundRestoreVerificationReportBindsBackupAndManifestEvidence(
	t *testing.T,
) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "restore-check.report")
	result := fixedRestoreReportResult()
	backupID := uuid.MustParse(
		"11111111-1111-4111-8111-111111111111",
	)
	manifestSHA256 := sha256.Sum256([]byte("canonical manifest bytes"))

	if err := WriteBoundRestoreVerificationReport(
		path,
		backupID,
		manifestSHA256,
		result,
	); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	evidenceInput := make([]byte, 0, 16+sha256.Size*2)
	evidenceInput = append(evidenceInput, backupID[:]...)
	evidenceInput = append(evidenceInput, manifestSHA256[:]...)
	evidenceInput = append(evidenceInput, result.ReportSHA256...)
	evidenceSHA256 := sha256.Sum256(evidenceInput)

	expected := strings.Replace(
		fixedRestoreReportText(result),
		"schema_version=1\n",
		"schema_version=1\n"+
			"backup_id="+backupID.String()+"\n"+
			"manifest_sha256="+hex.EncodeToString(manifestSHA256[:])+"\n",
		1,
	)
	expected = strings.Replace(
		expected,
		"verification_report_sha256="+
			hex.EncodeToString(result.ReportSHA256)+"\n",
		"verification_report_sha256="+
			hex.EncodeToString(result.ReportSHA256)+"\n"+
			"evidence_sha256="+
			hex.EncodeToString(evidenceSHA256[:])+"\n",
		1,
	)
	if string(content) != expected {
		t.Fatalf("report=%q want=%q", content, expected)
	}
}

func TestWriteBoundRestoreVerificationReportRejectsInvalidBinding(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		backupID     uuid.UUID
		manifestHash [sha256.Size]byte
	}{
		{
			name:         "nil backup ID",
			manifestHash: sha256.Sum256([]byte("manifest")),
		},
		{
			name: "non-v4 backup ID",
			backupID: uuid.MustParse(
				"11111111-1111-1111-8111-111111111111",
			),
			manifestHash: sha256.Sum256([]byte("manifest")),
		},
		{
			name: "zero manifest hash",
			backupID: uuid.MustParse(
				"11111111-1111-4111-8111-111111111111",
			),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Chmod(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "restore-check.report")
			err := WriteBoundRestoreVerificationReport(
				path,
				testCase.backupID,
				testCase.manifestHash,
				fixedRestoreReportResult(),
			)
			if !errors.Is(err, ErrRestoreReport) {
				t.Fatalf("error=%v", err)
			}
			if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
				t.Fatalf("invalid binding produced report: %v", statErr)
			}
		})
	}
}

func TestWriteRestoreVerificationReportRequiresOwned0700Directory(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "restore-check.report")
	err := WriteRestoreVerificationReport(path, fixedRestoreReportResult())
	if !errors.Is(err, ErrRestoreReport) {
		t.Fatalf("error=%v", err)
	}
	if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
		t.Fatalf("unsafe directory produced report: %v", statErr)
	}
}

func TestWriteRestoreVerificationReportNeverOverwritesExistingReport(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "restore-check.report")
	const original = "existing report must survive\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	err := WriteRestoreVerificationReport(path, fixedRestoreReportResult())
	if !errors.Is(err, ErrRestoreReport) {
		t.Fatalf("error=%v", err)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != original {
		t.Fatalf("existing report overwritten: %q", content)
	}
	if _, statErr := os.Lstat(filepath.Join(directory, ".restore-check.report.pending")); !os.IsNotExist(statErr) {
		t.Fatalf("temporary report remained: %v", statErr)
	}
}

func TestWriteRestoreVerificationReportUsesExclusivePendingFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(directory, "victim")
	if err := os.WriteFile(victim, []byte("do not overwrite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(directory, ".restore-check.report.pending")
	if err := os.Symlink(victim, pending); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(directory, "restore-check.report")
	err := WriteRestoreVerificationReport(report, fixedRestoreReportResult())
	if !errors.Is(err, ErrRestoreReport) {
		t.Fatalf("error=%v", err)
	}
	content, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "do not overwrite\n" {
		t.Fatalf("exclusive pending write followed symlink: %q", content)
	}
	if _, statErr := os.Lstat(report); !os.IsNotExist(statErr) {
		t.Fatalf("pending collision produced report: %v", statErr)
	}
}

func TestWriteRestoreVerificationReportRollsBackPostLinkFailures(t *testing.T) {
	injectedFailure := errors.New("injected filesystem failure")
	for _, testCase := range []struct {
		name   string
		inject func(restoreReportOperations) restoreReportOperations
	}{
		{
			name: "first directory sync",
			inject: func(operations restoreReportOperations) restoreReportOperations {
				syncCalls := 0
				realSync := operations.syncDirectory
				operations.syncDirectory = func(path string) error {
					syncCalls++
					if syncCalls == 1 {
						return injectedFailure
					}
					return realSync(path)
				}
				return operations
			},
		},
		{
			name: "second directory sync",
			inject: func(operations restoreReportOperations) restoreReportOperations {
				syncCalls := 0
				realSync := operations.syncDirectory
				operations.syncDirectory = func(path string) error {
					syncCalls++
					if syncCalls == 2 {
						return injectedFailure
					}
					return realSync(path)
				}
				return operations
			},
		},
		{
			name: "pending remove",
			inject: func(operations restoreReportOperations) restoreReportOperations {
				removeCalls := 0
				realRemove := operations.remove
				operations.remove = func(path string) error {
					removeCalls++
					if removeCalls == 1 {
						return injectedFailure
					}
					return realRemove(path)
				}
				return operations
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Chmod(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "restore-check.report")
			pending := filepath.Join(directory, ".restore-check.report.pending")
			operations := testCase.inject(defaultRestoreReportOperations())

			err := writeRestoreVerificationReport(
				path,
				fixedRestoreReportResult(),
				operations,
			)
			if !errors.Is(err, ErrRestoreReport) {
				t.Fatalf("error=%v", err)
			}
			if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
				t.Fatalf("failed write left final report: %v", statErr)
			}
			if _, statErr := os.Lstat(pending); !os.IsNotExist(statErr) {
				t.Fatalf("failed write left blocking pending report: %v", statErr)
			}

			if retryErr := WriteRestoreVerificationReport(
				path,
				fixedRestoreReportResult(),
			); retryErr != nil {
				t.Fatalf("safe retry failed: %v", retryErr)
			}
		})
	}
}

func TestWriteRestoreVerificationReportRollbackPreservesReplacement(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "restore-check.report")
	const replacement = "replacement owned by another writer\n"
	operations := defaultRestoreReportOperations()
	realSync := operations.syncDirectory
	syncCalls := 0
	operations.syncDirectory = func(directory string) error {
		syncCalls++
		if syncCalls != 1 {
			return realSync(directory)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
			t.Fatal(err)
		}
		return errors.New("injected directory sync failure")
	}

	err := writeRestoreVerificationReport(
		path,
		fixedRestoreReportResult(),
		operations,
	)
	if !errors.Is(err, ErrRestoreReport) {
		t.Fatalf("error=%v", err)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != replacement {
		t.Fatalf("rollback removed replacement: %q", content)
	}
	pending := filepath.Join(directory, ".restore-check.report.pending")
	if _, statErr := os.Lstat(pending); !os.IsNotExist(statErr) {
		t.Fatalf("rollback left pending report: %v", statErr)
	}
}

func TestWriteRestoreVerificationReportRejectsUnsafeOrIncompleteResult(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*RestoreVerificationResult)
	}{
		{
			name: "missing fixed table",
			mutate: func(result *RestoreVerificationResult) {
				delete(result.DatabaseRowCounts, "users")
			},
		},
		{
			name: "unexpected table",
			mutate: func(result *RestoreVerificationResult) {
				result.DatabaseRowCounts["credentials"] = 1
			},
		},
		{
			name: "bad hash",
			mutate: func(result *RestoreVerificationResult) {
				result.ReportSHA256 = []byte("secret-hash")
			},
		},
		{
			name: "session not verified",
			mutate: func(result *RestoreVerificationResult) {
				result.SessionRevocationVerified = false
			},
		},
		{
			name: "missing object",
			mutate: func(result *RestoreVerificationResult) {
				result.MissingObjectCount = 1
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Chmod(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "restore-check.report")
			result := fixedRestoreReportResult()
			testCase.mutate(&result)
			err := WriteRestoreVerificationReport(path, result)
			if !errors.Is(err, ErrRestoreReport) {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), "credentials") ||
				strings.Contains(err.Error(), directory) {
				t.Fatalf("error leaks unsafe detail: %v", err)
			}
			if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
				t.Fatalf("invalid result produced report: %v", statErr)
			}
		})
	}
}

func fixedRestoreReportResult() RestoreVerificationResult {
	counts := make(map[string]int64, len(restoreRowCountAllowlist))
	for index, table := range restoreRowCountAllowlist {
		counts[table] = int64(index + 1)
	}
	result := RestoreVerificationResult{
		RestoredMigrationVersion:  20,
		DatabaseRowCounts:         counts,
		CheckedObjectCount:        7,
		SessionRevocationVerified: true,
	}
	result.setReportSHA256()
	return result
}

func fixedRestoreReportText(result RestoreVerificationResult) string {
	return "schema_version=1\nmigration_version=20\n" +
		"table_users_count=1\n" +
		"table_sessions_count=2\n" +
		"table_subjects_count=3\n" +
		"table_grades_count=4\n" +
		"table_terms_count=5\n" +
		"table_chapters_count=6\n" +
		"table_lessons_count=7\n" +
		"table_lesson_revisions_count=8\n" +
		"table_files_count=9\n" +
		"table_file_versions_count=10\n" +
		"table_file_previews_count=11\n" +
		"table_qa_threads_count=12\n" +
		"table_qa_messages_count=13\n" +
		"table_ai_threads_count=14\n" +
		"table_ai_messages_count=15\n" +
		"table_ai_runs_count=16\n" +
		"row_count_total=136\n" +
		"checked_object_count=7\n" +
		"missing_object_count=0\n" +
		"unexpected_object_count=0\n" +
		"active_session_count=0\n" +
		"verification_report_sha256=" +
		hex.EncodeToString(result.ReportSHA256) + "\n"
}
