package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"syscall"

	"github.com/google/uuid"
)

var ErrRestoreReport = errors.New("restore verification report unavailable")

const maxRestoreReportBytes = 16 << 10

type restoreReportOperations struct {
	link          func(string, string) error
	remove        func(string) error
	syncDirectory func(string) error
}

func defaultRestoreReportOperations() restoreReportOperations {
	return restoreReportOperations{
		link:          os.Link,
		remove:        os.Remove,
		syncDirectory: syncRestoreReportDirectory,
	}
}

func WriteRestoreVerificationReport(
	path string,
	result RestoreVerificationResult,
) error {
	return writeRestoreVerificationReport(
		path,
		result,
		defaultRestoreReportOperations(),
	)
}

func WriteBoundRestoreVerificationReport(
	path string,
	backupID uuid.UUID,
	manifestSHA256 [sha256.Size]byte,
	result RestoreVerificationResult,
) error {
	content, err := marshalBoundRestoreVerificationReport(
		backupID,
		manifestSHA256,
		result,
	)
	if err != nil {
		return ErrRestoreReport
	}
	return writeRestoreVerificationReportContent(
		path,
		content,
		defaultRestoreReportOperations(),
	)
}

func writeRestoreVerificationReport(
	path string,
	result RestoreVerificationResult,
	operations restoreReportOperations,
) error {
	content, err := marshalRestoreVerificationReport(result)
	if err != nil {
		return ErrRestoreReport
	}
	return writeRestoreVerificationReportContent(path, content, operations)
}

func writeRestoreVerificationReportContent(
	path string,
	content []byte,
	operations restoreReportOperations,
) error {
	if operations.link == nil ||
		operations.remove == nil ||
		operations.syncDirectory == nil {
		return ErrRestoreReport
	}
	directory := filepath.Dir(path)
	if filepath.Base(path) != "restore-check.report" ||
		!ownedRestoreReportDirectory(directory) {
		return ErrRestoreReport
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return ErrRestoreReport
	}

	pending := filepath.Join(directory, ".restore-check.report.pending")
	file, err := os.OpenFile(
		pending,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return ErrRestoreReport
	}
	pendingInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return ErrRestoreReport
	}
	pendingExists := true
	finalLinked := false
	committed := false
	defer func() {
		_ = file.Close()
		directoryChanged := false
		if !committed && finalLinked &&
			removeRestoreReportIfSame(path, pendingInfo, operations.remove) {
			directoryChanged = true
		}
		if pendingExists {
			if removeRestoreReportIfSame(
				pending,
				pendingInfo,
				operations.remove,
			) {
				directoryChanged = true
			}
		}
		if directoryChanged {
			_ = operations.syncDirectory(directory)
		}
	}()

	if err := file.Chmod(0o600); err != nil {
		return ErrRestoreReport
	}
	safePendingInfo, err := file.Stat()
	if err != nil ||
		!os.SameFile(pendingInfo, safePendingInfo) ||
		!safeRestoreReportFileInfo(safePendingInfo) {
		return ErrRestoreReport
	}
	pendingInfo = safePendingInfo
	if writeRestoreReport(file, content) != nil ||
		file.Sync() != nil ||
		file.Close() != nil {
		return ErrRestoreReport
	}
	if err := operations.link(pending, path); err != nil {
		return ErrRestoreReport
	}
	finalLinked = true
	finalInfo, safe := safeRestoreReportFile(path)
	if !safe || !os.SameFile(pendingInfo, finalInfo) {
		return ErrRestoreReport
	}
	if err := operations.syncDirectory(directory); err != nil {
		return ErrRestoreReport
	}
	if err := operations.remove(pending); err != nil {
		return ErrRestoreReport
	}
	pendingExists = false
	if err := operations.syncDirectory(directory); err != nil {
		return ErrRestoreReport
	}
	committed = true
	return nil
}

func marshalRestoreVerificationReport(
	result RestoreVerificationResult,
) ([]byte, error) {
	return marshalRestoreVerificationReportWithBinding(
		result,
		nil,
	)
}

type restoreReportBinding struct {
	backupID       uuid.UUID
	manifestSHA256 [sha256.Size]byte
	evidenceSHA256 [sha256.Size]byte
}

func marshalBoundRestoreVerificationReport(
	backupID uuid.UUID,
	manifestSHA256 [sha256.Size]byte,
	result RestoreVerificationResult,
) ([]byte, error) {
	if backupID == uuid.Nil ||
		backupID.Version() != 4 ||
		backupID.Variant() != uuid.RFC4122 ||
		manifestSHA256 == [sha256.Size]byte{} ||
		len(result.ReportSHA256) != sha256.Size {
		return nil, ErrRestoreReport
	}
	evidenceInput := make([]byte, 0, 16+sha256.Size*2)
	evidenceInput = append(evidenceInput, backupID[:]...)
	evidenceInput = append(evidenceInput, manifestSHA256[:]...)
	evidenceInput = append(evidenceInput, result.ReportSHA256...)
	binding := &restoreReportBinding{
		backupID:       backupID,
		manifestSHA256: manifestSHA256,
		evidenceSHA256: sha256.Sum256(evidenceInput),
	}
	return marshalRestoreVerificationReportWithBinding(result, binding)
}

func marshalRestoreVerificationReportWithBinding(
	result RestoreVerificationResult,
	binding *restoreReportBinding,
) ([]byte, error) {
	if result.RestoredMigrationVersion < 1 ||
		!result.SessionRevocationVerified ||
		result.CheckedObjectCount < 0 ||
		result.MissingObjectCount != 0 ||
		result.UnexpectedObjectCount != 0 ||
		len(result.ReportSHA256) != 32 ||
		len(result.DatabaseRowCounts) != len(restoreRowCountAllowlist) {
		return nil, ErrRestoreReport
	}
	expectedHash := result
	expectedHash.setReportSHA256()
	if !reflect.DeepEqual(result.ReportSHA256, expectedHash.ReportSHA256) {
		return nil, ErrRestoreReport
	}

	var report bytes.Buffer
	_, _ = fmt.Fprintf(&report, "schema_version=%d\n", restoreReportFormatVersion)
	if binding != nil {
		_, _ = fmt.Fprintf(
			&report,
			"backup_id=%s\nmanifest_sha256=%s\n",
			binding.backupID.String(),
			hex.EncodeToString(binding.manifestSHA256[:]),
		)
	}
	_, _ = fmt.Fprintf(
		&report,
		"migration_version=%d\n",
		result.RestoredMigrationVersion,
	)
	var rowCountTotal int64
	for _, table := range restoreRowCountAllowlist {
		count, present := result.DatabaseRowCounts[table]
		if !present || count < 0 || rowCountTotal > math.MaxInt64-count {
			return nil, ErrRestoreReport
		}
		rowCountTotal += count
		_, _ = fmt.Fprintf(
			&report,
			"table_%s_count=%d\n",
			table,
			count,
		)
	}
	_, _ = fmt.Fprintf(
		&report,
		"row_count_total=%d\n"+
			"checked_object_count=%d\n"+
			"missing_object_count=%d\n"+
			"unexpected_object_count=%d\n"+
			"active_session_count=0\n"+
			"verification_report_sha256=%s\n",
		rowCountTotal,
		result.CheckedObjectCount,
		result.MissingObjectCount,
		result.UnexpectedObjectCount,
		hex.EncodeToString(result.ReportSHA256),
	)
	if binding != nil {
		_, _ = fmt.Fprintf(
			&report,
			"evidence_sha256=%s\n",
			hex.EncodeToString(binding.evidenceSHA256[:]),
		)
	}
	if report.Len() < 1 || report.Len() > maxRestoreReportBytes {
		return nil, ErrRestoreReport
	}
	return report.Bytes(), nil
}

func writeRestoreReport(file *os.File, content []byte) error {
	for len(content) > 0 {
		written, err := file.Write(content)
		if err != nil {
			return err
		}
		if written < 1 {
			return ErrRestoreReport
		}
		content = content[written:]
	}
	return nil
}

func ownedRestoreReportDirectory(path string) bool {
	info, err := os.Lstat(path)
	if err != nil ||
		!info.IsDir() ||
		info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func safeRestoreReportFile(path string) (os.FileInfo, bool) {
	info, err := os.Lstat(path)
	if err != nil || !safeRestoreReportFileInfo(info) {
		return nil, false
	}
	return info, true
}

func safeRestoreReportFileInfo(info os.FileInfo) bool {
	if info == nil ||
		!info.Mode().IsRegular() ||
		info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o600 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func removeRestoreReportIfSame(
	path string,
	expected os.FileInfo,
	remove func(string) error,
) bool {
	info, err := os.Lstat(path)
	if err != nil || !os.SameFile(expected, info) {
		return false
	}
	return remove(path) == nil
}

func syncRestoreReportDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
