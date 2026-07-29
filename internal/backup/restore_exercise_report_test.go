package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const (
	restoreExerciseVerificationID = "22222222-2222-4222-8222-222222222222"
	restoreExerciseBackupID       = "11111111-1111-4111-8111-111111111111"
)

func TestParseRestoreExerciseReportRequiresCanonicalBoundSuccess(t *testing.T) {
	raw := fixedRestoreExerciseReport()
	report, err := ParseRestoreExerciseReport(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if report.VerificationID.String() != restoreExerciseVerificationID ||
		report.BackupID.String() != restoreExerciseBackupID ||
		report.DurationSeconds != 54 ||
		report.RestoredMigrationVersion != 22 ||
		report.DatabaseRowCounts["users"] != 1 ||
		report.DatabaseRowCounts["ai_runs"] != 16 ||
		report.RowCountTotal != 136 ||
		report.CheckedObjectCount != 2 ||
		report.Isolation404ProbeCount != 2 ||
		hex.EncodeToString(report.ReportSHA256) != reportHashFromRaw(raw) {
		t.Fatalf("report=%+v", report)
	}
}

func TestParseRestoreExerciseReportRejectsNonCanonicalOrUnsafeEvidence(t *testing.T) {
	valid := fixedRestoreExerciseReport()
	for _, test := range []struct {
		name string
		raw  string
	}{
		{
			name: "unknown field",
			raw: strings.Replace(
				valid,
				`"schemaVersion":2,`,
				`"schemaVersion":2,"unknown":1,`,
				1,
			),
		},
		{
			name: "duplicate field",
			raw: strings.Replace(
				valid,
				`"verificationId":"`,
				`"verificationId":"`+restoreExerciseVerificationID+
					`","verificationId":"`,
				1,
			),
		},
		{
			name: "noncanonical whitespace",
			raw:  " " + valid,
		},
		{
			name: "unknown row count",
			raw: strings.Replace(
				valid,
				`"ai_runs":16}`,
				`"ai_runs":16,"secrets":1}`,
				1,
			),
		},
		{
			name: "missing row count",
			raw:  strings.Replace(valid, `,"ai_runs":16`, "", 1),
		},
		{
			name: "row total mismatch",
			raw:  strings.Replace(valid, `"rowCountTotal":136`, `"rowCountTotal":135`, 1),
		},
		{
			name: "active session",
			raw: strings.Replace(
				valid,
				`"activeSessionCount":0`,
				`"activeSessionCount":1`,
				1,
			),
		},
		{
			name: "missing object",
			raw: strings.Replace(
				valid,
				`"missingObjectCount":0`,
				`"missingObjectCount":1`,
				1,
			),
		},
		{
			name: "wrong report hash",
			raw: strings.Replace(
				valid,
				`"reportSHA256":"`+reportHashFromRaw(valid),
				`"reportSHA256":"`+strings.Repeat("f", 64),
				1,
			),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseRestoreExerciseReport(strings.NewReader(test.raw))
			if !errors.Is(err, ErrRestoreExerciseReport) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func fixedRestoreExerciseReport() string {
	counts := `{"users":1,"sessions":2,"subjects":3,"grades":4,` +
		`"terms":5,"chapters":6,"lessons":7,"lesson_revisions":8,` +
		`"files":9,"file_versions":10,"file_previews":11,` +
		`"qa_threads":12,"qa_messages":13,"ai_threads":14,` +
		`"ai_messages":15,"ai_runs":16}`
	manifest := strings.Repeat("a", 64)
	verification := strings.Repeat("b", 64)
	evidence := strings.Repeat("c", 64)
	canonical := fmt.Sprintf(
		"schemaVersion=2\n"+
			"verificationId=%s\n"+
			"backupId=%s\n"+
			"manifestSHA256=%s\n"+
			"verificationReportSHA256=%s\n"+
			"evidenceSHA256=%s\n"+
			"durationSeconds=54\n"+
			"migrationVersion=22\n"+
			"databaseRowCounts=%s\n"+
			"rowCountTotal=136\n"+
			"checkedObjectCount=2\n"+
			"missingObjectCount=0\n"+
			"unexpectedObjectCount=0\n"+
			"activeSessionCount=0\n"+
			"isolation404ProbeCount=2\n",
		restoreExerciseVerificationID,
		restoreExerciseBackupID,
		manifest,
		verification,
		evidence,
		counts,
	)
	reportHash := sha256.Sum256([]byte(canonical))
	return fmt.Sprintf(
		`{"schemaVersion":2,"verificationId":"%s","backupId":"%s",`+
			`"manifestSHA256":"%s","verificationReportSHA256":"%s",`+
			`"evidenceSHA256":"%s","durationSeconds":54,`+
			`"migrationVersion":22,"databaseRowCounts":%s,`+
			`"rowCountTotal":136,"checkedObjectCount":2,`+
			`"missingObjectCount":0,"unexpectedObjectCount":0,`+
			`"activeSessionCount":0,"isolation404ProbeCount":2,`+
			`"reportSHA256":"%s"}`+"\n",
		restoreExerciseVerificationID,
		restoreExerciseBackupID,
		manifest,
		verification,
		evidence,
		counts,
		hex.EncodeToString(reportHash[:]),
	)
}

func reportHashFromRaw(raw string) string {
	const marker = `"reportSHA256":"`
	start := strings.Index(raw, marker)
	if start < 0 {
		return ""
	}
	start += len(marker)
	if len(raw) < start+64 {
		return ""
	}
	return raw[start : start+64]
}
