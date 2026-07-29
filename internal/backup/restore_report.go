package backup

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
)

var ErrRestoreReport = errors.New("restore verification report unavailable")

const maxRestoreReportBytes = 16 << 10

func WriteRestoreVerificationReport(
	path string,
	result RestoreVerificationResult,
) error {
	content, err := marshalRestoreVerificationReport(result)
	if err != nil {
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
	pendingExists := true
	defer func() {
		_ = file.Close()
		if pendingExists {
			_ = os.Remove(pending)
		}
	}()

	if err := file.Chmod(0o600); err != nil ||
		writeRestoreReport(file, content) != nil ||
		file.Sync() != nil ||
		file.Close() != nil {
		return ErrRestoreReport
	}
	if err := os.Link(pending, path); err != nil {
		return ErrRestoreReport
	}
	if !safeRestoreReportFile(path) {
		_ = os.Remove(path)
		return ErrRestoreReport
	}
	if err := syncRestoreReportDirectory(directory); err != nil {
		return ErrRestoreReport
	}
	if err := os.Remove(pending); err != nil {
		return ErrRestoreReport
	}
	pendingExists = false
	if err := syncRestoreReportDirectory(directory); err != nil {
		return ErrRestoreReport
	}
	return nil
}

func marshalRestoreVerificationReport(
	result RestoreVerificationResult,
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
	_, _ = fmt.Fprintf(
		&report,
		"schema_version=%d\nmigration_version=%d\n",
		restoreReportFormatVersion,
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

func safeRestoreReportFile(path string) bool {
	info, err := os.Lstat(path)
	if err != nil ||
		!info.Mode().IsRegular() ||
		info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o600 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func syncRestoreReportDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
