package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"

	"github.com/google/uuid"
)

var ErrRestoreExerciseReport = errors.New("restore exercise report unavailable")

const (
	restoreExerciseSchemaVersion  = 2
	maxRestoreExerciseReportBytes = 4096
	restoreExerciseRTOLimit       = 14400
)

var restoreExerciseSHA256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type RestoreExerciseReport struct {
	VerificationID           uuid.UUID
	BackupID                 uuid.UUID
	ManifestSHA256           []byte
	VerificationReportSHA256 []byte
	EvidenceSHA256           []byte
	DurationSeconds          int64
	RestoredMigrationVersion int64
	DatabaseRowCounts        map[string]int64
	RowCountTotal            int64
	CheckedObjectCount       int64
	MissingObjectCount       int64
	UnexpectedObjectCount    int64
	ActiveSessionCount       int64
	Isolation404ProbeCount   int64
	ReportSHA256             []byte
}

type restoreExerciseReportWire struct {
	SchemaVersion            int              `json:"schemaVersion"`
	VerificationID           string           `json:"verificationId"`
	BackupID                 string           `json:"backupId"`
	ManifestSHA256           string           `json:"manifestSHA256"`
	VerificationReportSHA256 string           `json:"verificationReportSHA256"`
	EvidenceSHA256           string           `json:"evidenceSHA256"`
	DurationSeconds          int64            `json:"durationSeconds"`
	MigrationVersion         int64            `json:"migrationVersion"`
	DatabaseRowCounts        map[string]int64 `json:"databaseRowCounts"`
	RowCountTotal            int64            `json:"rowCountTotal"`
	CheckedObjectCount       int64            `json:"checkedObjectCount"`
	MissingObjectCount       int64            `json:"missingObjectCount"`
	UnexpectedObjectCount    int64            `json:"unexpectedObjectCount"`
	ActiveSessionCount       int64            `json:"activeSessionCount"`
	Isolation404ProbeCount   int64            `json:"isolation404ProbeCount"`
	ReportSHA256             string           `json:"reportSHA256"`
}

func ParseRestoreExerciseReport(reader io.Reader) (RestoreExerciseReport, error) {
	if reader == nil {
		return RestoreExerciseReport{}, ErrRestoreExerciseReport
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxRestoreExerciseReportBytes+1))
	if err != nil ||
		len(raw) < 2 ||
		len(raw) > maxRestoreExerciseReportBytes ||
		raw[len(raw)-1] != '\n' ||
		bytes.Count(raw, []byte{'\n'}) != 1 {
		return RestoreExerciseReport{}, ErrRestoreExerciseReport
	}

	var wire restoreExerciseReportWire
	decoder := json.NewDecoder(bytes.NewReader(raw[:len(raw)-1]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return RestoreExerciseReport{}, ErrRestoreExerciseReport
	}
	if err := requireRestoreExerciseJSONEOF(decoder); err != nil {
		return RestoreExerciseReport{}, ErrRestoreExerciseReport
	}

	report, err := validateRestoreExerciseWire(wire)
	if err != nil {
		return RestoreExerciseReport{}, ErrRestoreExerciseReport
	}
	expected, err := marshalRestoreExerciseJSON(report)
	if err != nil || !bytes.Equal(raw, expected) {
		return RestoreExerciseReport{}, ErrRestoreExerciseReport
	}
	return report, nil
}

func requireRestoreExerciseJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrRestoreExerciseReport
	}
	return nil
}

func validateRestoreExerciseWire(
	wire restoreExerciseReportWire,
) (RestoreExerciseReport, error) {
	verificationID, err := parseRestoreExerciseUUID(wire.VerificationID)
	if err != nil {
		return RestoreExerciseReport{}, err
	}
	backupID, err := parseRestoreExerciseUUID(wire.BackupID)
	if err != nil {
		return RestoreExerciseReport{}, err
	}
	manifest, err := parseRestoreExerciseSHA256(wire.ManifestSHA256)
	if err != nil {
		return RestoreExerciseReport{}, err
	}
	verification, err := parseRestoreExerciseSHA256(
		wire.VerificationReportSHA256,
	)
	if err != nil {
		return RestoreExerciseReport{}, err
	}
	evidence, err := parseRestoreExerciseSHA256(wire.EvidenceSHA256)
	if err != nil {
		return RestoreExerciseReport{}, err
	}
	reportSHA256, err := parseRestoreExerciseSHA256(wire.ReportSHA256)
	if err != nil {
		return RestoreExerciseReport{}, err
	}
	counts, total, err := validateRestoreExerciseRowCounts(
		wire.DatabaseRowCounts,
	)
	if err != nil ||
		wire.SchemaVersion != restoreExerciseSchemaVersion ||
		wire.DurationSeconds < 0 ||
		wire.DurationSeconds >= restoreExerciseRTOLimit ||
		wire.MigrationVersion < 1 ||
		wire.RowCountTotal != total ||
		wire.CheckedObjectCount < 0 ||
		wire.MissingObjectCount != 0 ||
		wire.UnexpectedObjectCount != 0 ||
		wire.ActiveSessionCount != 0 ||
		wire.Isolation404ProbeCount != 2 {
		return RestoreExerciseReport{}, ErrRestoreExerciseReport
	}
	result := RestoreExerciseReport{
		VerificationID:           verificationID,
		BackupID:                 backupID,
		ManifestSHA256:           manifest,
		VerificationReportSHA256: verification,
		EvidenceSHA256:           evidence,
		DurationSeconds:          wire.DurationSeconds,
		RestoredMigrationVersion: wire.MigrationVersion,
		DatabaseRowCounts:        counts,
		RowCountTotal:            total,
		CheckedObjectCount:       wire.CheckedObjectCount,
		MissingObjectCount:       wire.MissingObjectCount,
		UnexpectedObjectCount:    wire.UnexpectedObjectCount,
		ActiveSessionCount:       wire.ActiveSessionCount,
		Isolation404ProbeCount:   wire.Isolation404ProbeCount,
		ReportSHA256:             reportSHA256,
	}
	calculated, err := restoreExerciseCanonicalSHA256(result)
	if err != nil || !bytes.Equal(result.ReportSHA256, calculated[:]) {
		return RestoreExerciseReport{}, ErrRestoreExerciseReport
	}
	return result, nil
}

func parseRestoreExerciseUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil ||
		parsed == uuid.Nil ||
		parsed.Version() != 4 ||
		parsed.Variant() != uuid.RFC4122 ||
		parsed.String() != value {
		return uuid.Nil, ErrRestoreExerciseReport
	}
	return parsed, nil
}

func parseRestoreExerciseSHA256(value string) ([]byte, error) {
	if !restoreExerciseSHA256Pattern.MatchString(value) {
		return nil, ErrRestoreExerciseReport
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return nil, ErrRestoreExerciseReport
	}
	return decoded, nil
}

func validateRestoreExerciseRowCounts(
	input map[string]int64,
) (map[string]int64, int64, error) {
	if len(input) != len(restoreRowCountAllowlist) {
		return nil, 0, ErrRestoreExerciseReport
	}
	counts := make(map[string]int64, len(input))
	var total int64
	for _, table := range restoreRowCountAllowlist {
		value, ok := input[table]
		if !ok || value < 0 || total > math.MaxInt64-value {
			return nil, 0, ErrRestoreExerciseReport
		}
		counts[table] = value
		total += value
	}
	return counts, total, nil
}

func restoreExerciseCanonicalSHA256(
	report RestoreExerciseReport,
) ([sha256.Size]byte, error) {
	counts, total, err := validateRestoreExerciseRowCounts(
		report.DatabaseRowCounts,
	)
	if err != nil || total != report.RowCountTotal {
		return [sha256.Size]byte{}, ErrRestoreExerciseReport
	}
	var canonical bytes.Buffer
	_, _ = fmt.Fprintf(
		&canonical,
		"schemaVersion=%d\n"+
			"verificationId=%s\n"+
			"backupId=%s\n"+
			"manifestSHA256=%s\n"+
			"verificationReportSHA256=%s\n"+
			"evidenceSHA256=%s\n"+
			"durationSeconds=%d\n"+
			"migrationVersion=%d\n"+
			"databaseRowCounts=",
		restoreExerciseSchemaVersion,
		report.VerificationID,
		report.BackupID,
		hex.EncodeToString(report.ManifestSHA256),
		hex.EncodeToString(report.VerificationReportSHA256),
		hex.EncodeToString(report.EvidenceSHA256),
		report.DurationSeconds,
		report.RestoredMigrationVersion,
	)
	writeRestoreExerciseCounts(&canonical, counts)
	_, _ = fmt.Fprintf(
		&canonical,
		"\nrowCountTotal=%d\n"+
			"checkedObjectCount=%d\n"+
			"missingObjectCount=%d\n"+
			"unexpectedObjectCount=%d\n"+
			"activeSessionCount=%d\n"+
			"isolation404ProbeCount=%d\n",
		report.RowCountTotal,
		report.CheckedObjectCount,
		report.MissingObjectCount,
		report.UnexpectedObjectCount,
		report.ActiveSessionCount,
		report.Isolation404ProbeCount,
	)
	return sha256.Sum256(canonical.Bytes()), nil
}

func marshalRestoreExerciseJSON(
	report RestoreExerciseReport,
) ([]byte, error) {
	counts, total, err := validateRestoreExerciseRowCounts(
		report.DatabaseRowCounts,
	)
	if err != nil || total != report.RowCountTotal {
		return nil, ErrRestoreExerciseReport
	}
	var output bytes.Buffer
	_, _ = fmt.Fprintf(
		&output,
		`{"schemaVersion":%d,"verificationId":"%s","backupId":"%s",`+
			`"manifestSHA256":"%s","verificationReportSHA256":"%s",`+
			`"evidenceSHA256":"%s","durationSeconds":%d,`+
			`"migrationVersion":%d,"databaseRowCounts":`,
		restoreExerciseSchemaVersion,
		report.VerificationID,
		report.BackupID,
		hex.EncodeToString(report.ManifestSHA256),
		hex.EncodeToString(report.VerificationReportSHA256),
		hex.EncodeToString(report.EvidenceSHA256),
		report.DurationSeconds,
		report.RestoredMigrationVersion,
	)
	writeRestoreExerciseCounts(&output, counts)
	_, _ = fmt.Fprintf(
		&output,
		`,"rowCountTotal":%d,"checkedObjectCount":%d,`+
			`"missingObjectCount":%d,"unexpectedObjectCount":%d,`+
			`"activeSessionCount":%d,"isolation404ProbeCount":%d,`+
			`"reportSHA256":"%s"}`+"\n",
		report.RowCountTotal,
		report.CheckedObjectCount,
		report.MissingObjectCount,
		report.UnexpectedObjectCount,
		report.ActiveSessionCount,
		report.Isolation404ProbeCount,
		hex.EncodeToString(report.ReportSHA256),
	)
	if output.Len() < 2 || output.Len() > maxRestoreExerciseReportBytes {
		return nil, ErrRestoreExerciseReport
	}
	return output.Bytes(), nil
}

func writeRestoreExerciseCounts(
	output *bytes.Buffer,
	counts map[string]int64,
) {
	_ = output.WriteByte('{')
	for index, table := range restoreRowCountAllowlist {
		if index > 0 {
			_ = output.WriteByte(',')
		}
		_, _ = fmt.Fprintf(output, `"%s":%d`, table, counts[table])
	}
	_ = output.WriteByte('}')
}
