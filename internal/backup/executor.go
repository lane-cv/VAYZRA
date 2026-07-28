package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
)

const (
	CommandOutputLimit = 1 << 20

	defaultSecretRoot = "/run/secrets"
	pgDumpExecutable  = "/usr/bin/pg_dump"
	resticExecutable  = "/usr/local/bin/restic"
	ageExecutable     = "/usr/local/bin/age"
)

var (
	ErrCommandFailed      = errors.New("backup command failed")
	ErrCommandOutputLimit = errors.New("backup command output limit exceeded")
	ErrSecretUnavailable  = errors.New("backup secret unavailable")
	ErrExecutorConfig     = errors.New("invalid backup executor configuration")
	ErrDatabaseDump       = errors.New("database dump failed")
	ErrSnapshot           = errors.New("backup snapshot failed")
	ErrIntegrity          = errors.New("backup integrity verification failed")
	ErrRemoteSync         = errors.New("backup remote sync failed")
	ErrCapacity           = errors.New("backup plaintext capacity exceeded")
	ErrCleanup            = errors.New("backup temporary cleanup failed")
	ErrCancelled          = errors.New("backup cancelled")

	safeDatabaseName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	safeDatabaseHost = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)
	safeAgeRecipient = regexp.MustCompile(`^age1[023456789ac-hj-np-z]{20,100}$`)
)

type StdinMode uint8

const ClosedStdin StdinMode = 0

type Command struct {
	Executable  string
	Args        []string
	Env         []string
	Dir         string
	Stdin       StdinMode
	StdoutFile  string
	StdoutLimit int
	StderrLimit int
}

type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type Runner interface {
	Run(context.Context, Command) (CommandResult, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, command Command) (CommandResult, error) {
	if ctx == nil ||
		command.Executable == "" ||
		!filepath.IsAbs(command.Executable) ||
		command.Stdin != ClosedStdin ||
		command.StdoutLimit < 1 ||
		command.StderrLimit < 1 {
		return CommandResult{ExitCode: -1}, ErrCommandFailed
	}
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	process := exec.CommandContext(commandCtx, command.Executable, command.Args...)
	process.Args = append([]string{command.Executable}, command.Args...)
	process.Env = append([]string(nil), command.Env...)
	process.Dir = command.Dir
	process.Stdin = nil
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	process.WaitDelay = 2 * time.Second
	process.Cancel = func() error {
		if process.Process == nil {
			return nil
		}
		err := syscall.Kill(-process.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}

	var exceeded atomic.Bool
	stdoutBuffer := &boundedCommandWriter{
		limit: command.StdoutLimit, exceeded: &exceeded, cancel: cancel,
	}
	stderrBuffer := &boundedCommandWriter{
		limit: command.StderrLimit, exceeded: &exceeded, cancel: cancel,
	}
	var stdoutFile *os.File
	if command.StdoutFile != "" {
		var err error
		stdoutFile, err = os.OpenFile(
			command.StdoutFile,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			0o600,
		)
		if err != nil {
			return CommandResult{ExitCode: -1}, ErrCommandFailed
		}
		defer stdoutFile.Close()
		stdoutBuffer.destination = stdoutFile
	}
	process.Stdout = stdoutBuffer
	process.Stderr = stderrBuffer
	runErr := process.Run()
	result := CommandResult{
		Stdout:   stdoutBuffer.Bytes(),
		Stderr:   stderrBuffer.Bytes(),
		ExitCode: commandExitCode(runErr),
	}
	if closeErr := closeCommandOutput(stdoutFile); closeErr != nil {
		return result, ErrCommandFailed
	}
	stdoutFile = nil
	if exceeded.Load() {
		return result, ErrCommandOutputLimit
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return result, nil
	}
	if runErr != nil {
		return result, ErrCommandFailed
	}
	return result, nil
}

func closeCommandOutput(file *os.File) error {
	if file == nil {
		return nil
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

type boundedCommandWriter struct {
	buffer      bytes.Buffer
	destination io.Writer
	limit       int
	written     int
	exceeded    *atomic.Bool
	cancel      context.CancelFunc
}

func (writer *boundedCommandWriter) Write(value []byte) (int, error) {
	remaining := writer.limit - writer.written
	if remaining <= 0 {
		writer.exceeded.Store(true)
		writer.cancel()
		return len(value), nil
	}
	writeValue := value
	if len(writeValue) > remaining {
		writeValue = writeValue[:remaining]
		writer.exceeded.Store(true)
		writer.cancel()
	}
	var err error
	if writer.destination != nil {
		_, err = writer.destination.Write(writeValue)
	} else {
		_, err = writer.buffer.Write(writeValue)
	}
	writer.written += len(writeValue)
	return len(value), err
}

func (writer *boundedCommandWriter) Bytes() []byte {
	return append([]byte(nil), writer.buffer.Bytes()...)
}

type SecretName string

const (
	SecretDatabasePassword SecretName = "database_password"
	SecretLocalRepository  SecretName = "local_repository"
	SecretLocalPassword    SecretName = "local_password"
	SecretRemoteRepository SecretName = "remote_repository"
	SecretRemotePassword   SecretName = "remote_password"
)

type SecretSource interface {
	Read(SecretName) (string, error)
}

type FileSecrets struct {
	root string
}

func NewFileSecrets() FileSecrets {
	return fileSecretsAt(defaultSecretRoot)
}

func fileSecretsAt(root string) FileSecrets {
	return FileSecrets{root: root}
}

func (secrets FileSecrets) Read(name SecretName) (string, error) {
	if !validSecretName(name) || !filepath.IsAbs(secrets.root) {
		return "", ErrSecretUnavailable
	}
	rootInfo, err := os.Lstat(secrets.root)
	if err != nil ||
		!rootInfo.IsDir() ||
		rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", ErrSecretUnavailable
	}
	path := filepath.Join(secrets.root, string(name))
	info, err := os.Lstat(path)
	if err != nil ||
		!info.Mode().IsRegular() ||
		info.Mode().Perm() != 0o400 ||
		info.Mode()&os.ModeSymlink != 0 ||
		!ownedByCurrentUser(info) {
		return "", ErrSecretUnavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return "", ErrSecretUnavailable
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return "", ErrSecretUnavailable
	}
	contents, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(contents) == 0 || len(contents) > 4096 {
		return "", ErrSecretUnavailable
	}
	value := strings.TrimSuffix(string(contents), "\n")
	if value == "" ||
		strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "\x00\r\n") {
		return "", ErrSecretUnavailable
	}
	return value, nil
}

func validSecretName(name SecretName) bool {
	switch name {
	case SecretDatabasePassword, SecretLocalRepository, SecretLocalPassword,
		SecretRemoteRepository, SecretRemotePassword:
		return true
	default:
		return false
	}
}

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

type ExecutorConfig struct {
	Runner            Runner
	Secrets           SecretSource
	WorkRoot          string
	ObjectRoot        string
	DatabaseHost      string
	DatabasePort      string
	DatabaseUser      string
	DatabaseName      string
	DatabaseSSLMode   string
	AgeRecipient      string
	EncryptionKeyID   string
	Now               func() time.Time
	MaxPlaintextBytes int
}

type Executor struct {
	config ExecutorConfig
}

func NewExecutor(config ExecutorConfig) (Executor, error) {
	port, portErr := strconv.ParseUint(config.DatabasePort, 10, 16)
	if config.Runner == nil ||
		config.Secrets == nil ||
		!filepath.IsAbs(config.WorkRoot) ||
		!filepath.IsAbs(config.ObjectRoot) ||
		filepath.Clean(config.WorkRoot) == filepath.Clean(config.ObjectRoot) ||
		!safeDatabaseHost.MatchString(config.DatabaseHost) ||
		portErr != nil ||
		port == 0 ||
		!safeDatabaseName.MatchString(config.DatabaseUser) ||
		!safeDatabaseName.MatchString(config.DatabaseName) ||
		!validSSLMode(config.DatabaseSSLMode) ||
		!safeAgeRecipient.MatchString(config.AgeRecipient) ||
		!safeOpaqueValue.MatchString(config.EncryptionKeyID) ||
		config.MaxPlaintextBytes < ManifestMaxBytes {
		return Executor{}, ErrExecutorConfig
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	config.WorkRoot = filepath.Clean(config.WorkRoot)
	config.ObjectRoot = filepath.Clean(config.ObjectRoot)
	return Executor{config: config}, nil
}

func validSSLMode(mode string) bool {
	switch mode {
	case "require", "verify-ca", "verify-full":
		return true
	default:
		return false
	}
}

type SnapshotInput struct {
	RunID                    string
	DatabaseMigrationVersion int64
}

type SnapshotResult struct {
	Manifest          Manifest
	ManifestSHA256    [sha256.Size]byte
	EncryptionKeyID   string
	LocalSnapshotID   string
	DatabaseDumpBytes int64
	LogicalBytes      int64
	StoredBytes       int64
}

type VerifyInput struct {
	RunID          string
	SnapshotID     string
	ManifestSHA256 [sha256.Size]byte
}

type VerifyResult struct {
	Manifest Manifest
}

func (executor Executor) Snapshot(
	ctx context.Context,
	input SnapshotInput,
) (result SnapshotResult, resultErr error) {
	if !canonicalRunID(input.RunID) || input.DatabaseMigrationVersion < 1 {
		return SnapshotResult{}, ErrSnapshot
	}
	if err := secureDirectory(executor.config.WorkRoot); err != nil {
		return SnapshotResult{}, ErrSnapshot
	}
	if err := sourceDirectory(executor.config.ObjectRoot); err != nil {
		return SnapshotResult{}, ErrSnapshot
	}
	databasePassword, err := executor.config.Secrets.Read(SecretDatabasePassword)
	if err != nil {
		return SnapshotResult{}, ErrSnapshot
	}
	repository, err := executor.config.Secrets.Read(SecretLocalRepository)
	if err != nil {
		return SnapshotResult{}, ErrSnapshot
	}
	repositoryPassword, err := executor.config.Secrets.Read(SecretLocalPassword)
	if err != nil {
		return SnapshotResult{}, ErrSnapshot
	}
	workDirectory, err := os.MkdirTemp(executor.config.WorkRoot, ".backup-")
	if err != nil {
		return SnapshotResult{}, ErrSnapshot
	}
	if err := os.Chmod(workDirectory, 0o700); err != nil {
		_ = os.RemoveAll(workDirectory)
		return SnapshotResult{}, ErrSnapshot
	}
	defer func() {
		if err := cleanupWorkDirectory(executor.config.WorkRoot, workDirectory); err != nil {
			result = SnapshotResult{}
			resultErr = ErrCleanup
		}
	}()

	dumpPath := filepath.Join(workDirectory, "database.dump")
	pgResult, err := executor.config.Runner.Run(ctx, Command{
		Executable: pgDumpExecutable,
		Args: []string{
			"--format=custom",
			"--no-owner",
			"--no-privileges",
		},
		Env: []string{
			"LC_ALL=C",
			"PGHOST=" + executor.config.DatabaseHost,
			"PGPORT=" + executor.config.DatabasePort,
			"PGUSER=" + executor.config.DatabaseUser,
			"PGDATABASE=" + executor.config.DatabaseName,
			"PGSSLMODE=" + executor.config.DatabaseSSLMode,
			"PGPASSWORD=" + databasePassword,
		},
		Dir:         workDirectory,
		Stdin:       ClosedStdin,
		StdoutFile:  dumpPath,
		StdoutLimit: executor.config.MaxPlaintextBytes,
		StderrLimit: CommandOutputLimit,
	})
	if err != nil {
		return SnapshotResult{}, mapExecutorRunError(ctx, err, ErrDatabaseDump)
	}
	if pgResult.ExitCode != 0 {
		return SnapshotResult{}, ErrDatabaseDump
	}
	dumpHash, dumpBytes, err := hashBoundedRegularFile(
		dumpPath,
		int64(executor.config.MaxPlaintextBytes),
	)
	if err != nil {
		return SnapshotResult{}, ErrDatabaseDump
	}

	objectCount, referencedBytes, objectIdentity, err :=
		summarizeObjectFiles(executor.config.ObjectRoot)
	if err != nil {
		return SnapshotResult{}, ErrSnapshot
	}
	manifest := Manifest{
		SchemaVersion:            1,
		BatchID:                  input.RunID,
		CreatedAt:                executor.config.Now().UTC(),
		DatabaseMigrationVersion: input.DatabaseMigrationVersion,
		DatabaseDumpSHA256:       hex.EncodeToString(dumpHash[:]),
		ObjectSnapshotID:         hex.EncodeToString(objectIdentity[:]),
		ObjectCount:              objectCount,
		ReferencedBytes:          referencedBytes,
	}
	manifestBytes, err := MarshalManifest(manifest)
	if err != nil {
		return SnapshotResult{}, ErrSnapshot
	}
	manifestHash := sha256.Sum256(manifestBytes)
	manifestPath := filepath.Join(workDirectory, "manifest.json")
	if err := writeOwnerOnlyFile(manifestPath, manifestBytes); err != nil {
		return SnapshotResult{}, ErrSnapshot
	}
	recoveryBundleBytes, err := json.Marshal(recoveryBundle{
		SchemaVersion: 1,
		Repository:    repository,
		Manifest:      manifest,
		Instructions: []string{
			"restic check",
			"restic restore",
			"pg_restore",
		},
	})
	if err != nil || len(recoveryBundleBytes) > ManifestMaxBytes {
		return SnapshotResult{}, ErrSnapshot
	}
	recoveryBundlePath := filepath.Join(workDirectory, "recovery-bundle.json")
	if err := writeOwnerOnlyFile(recoveryBundlePath, recoveryBundleBytes); err != nil {
		return SnapshotResult{}, ErrSnapshot
	}
	bundlePath := filepath.Join(workDirectory, "recovery-bundle.age")
	ageResult, err := executor.config.Runner.Run(ctx, Command{
		Executable: ageExecutable,
		Args: []string{
			"--encrypt",
			"--recipient",
			executor.config.AgeRecipient,
			recoveryBundlePath,
		},
		Env:         []string{"LC_ALL=C"},
		Dir:         workDirectory,
		Stdin:       ClosedStdin,
		StdoutFile:  bundlePath,
		StdoutLimit: ManifestMaxBytes * 2,
		StderrLimit: CommandOutputLimit,
	})
	if err != nil {
		return SnapshotResult{}, mapExecutorRunError(ctx, err, ErrSnapshot)
	}
	if ageResult.ExitCode != 0 {
		return SnapshotResult{}, ErrSnapshot
	}
	snapshotSummary, err := executor.resticBackup(
		ctx,
		workDirectory,
		repository,
		repositoryPassword,
		[]string{
			"happylearn-batch:" + input.RunID,
			"happylearn-manifest-sha256:" + hex.EncodeToString(manifestHash[:]),
		},
		[]string{
			filepath.Base(dumpPath),
			filepath.Base(manifestPath),
			filepath.Base(bundlePath),
			executor.config.ObjectRoot,
		},
	)
	if err != nil {
		return SnapshotResult{}, err
	}
	logicalBytes := dumpBytes + referencedBytes
	if logicalBytes < dumpBytes {
		return SnapshotResult{}, ErrCapacity
	}
	return SnapshotResult{
		Manifest:          manifest,
		ManifestSHA256:    manifestHash,
		EncryptionKeyID:   executor.config.EncryptionKeyID,
		LocalSnapshotID:   snapshotSummary.SnapshotID,
		DatabaseDumpBytes: dumpBytes,
		LogicalBytes:      logicalBytes,
		StoredBytes:       snapshotSummary.DataAddedPacked,
	}, nil
}

type recoveryBundle struct {
	SchemaVersion int      `json:"schemaVersion"`
	Repository    string   `json:"repository"`
	Manifest      Manifest `json:"manifest"`
	Instructions  []string `json:"instructions"`
}

func (executor Executor) resticBackup(
	ctx context.Context,
	workDirectory string,
	repository string,
	password string,
	tags []string,
	paths []string,
) (resticSummary, error) {
	args := []string{
		"backup",
		"--json",
		"--quiet",
	}
	for _, tag := range tags {
		if tag == "" || strings.TrimSpace(tag) != tag {
			return resticSummary{}, ErrSnapshot
		}
		args = append(args, "--tag", tag)
	}
	args = append(args, paths...)
	result, err := executor.config.Runner.Run(ctx, Command{
		Executable: resticExecutable,
		Args:       args,
		Env: []string{
			"LC_ALL=C",
			"RESTIC_REPOSITORY=" + repository,
			"RESTIC_PASSWORD=" + password,
		},
		Dir:         workDirectory,
		Stdin:       ClosedStdin,
		StdoutLimit: CommandOutputLimit,
		StderrLimit: CommandOutputLimit,
	})
	if err != nil {
		return resticSummary{}, mapExecutorRunError(ctx, err, ErrSnapshot)
	}
	if result.ExitCode != 0 {
		return resticSummary{}, ErrSnapshot
	}
	summary, err := decodeResticSummary(result.Stdout)
	if err != nil {
		return resticSummary{}, ErrSnapshot
	}
	return summary, nil
}

func (executor Executor) Verify(
	ctx context.Context,
	input VerifyInput,
) (VerifyResult, error) {
	if !canonicalRunID(input.RunID) ||
		!resticSnapshotID.MatchString(input.SnapshotID) ||
		len(input.SnapshotID) != sha256.Size*2 {
		return VerifyResult{}, ErrIntegrity
	}
	repository, err := executor.config.Secrets.Read(SecretLocalRepository)
	if err != nil {
		return VerifyResult{}, ErrIntegrity
	}
	password, err := executor.config.Secrets.Read(SecretLocalPassword)
	if err != nil {
		return VerifyResult{}, ErrIntegrity
	}
	var manifestBytes []byte
	for _, args := range [][]string{
		{"check", "--read-data"},
		{"snapshots", "--json", input.SnapshotID},
		{"stats", "--json", "--mode=raw-data", input.SnapshotID},
		{"dump", input.SnapshotID, "manifest.json"},
	} {
		stdoutLimit := CommandOutputLimit
		if args[0] == "dump" {
			stdoutLimit = ManifestMaxBytes
		}
		result, runErr := executor.config.Runner.Run(ctx, Command{
			Executable: resticExecutable,
			Args:       args,
			Env: []string{
				"LC_ALL=C",
				"RESTIC_REPOSITORY=" + repository,
				"RESTIC_PASSWORD=" + password,
			},
			Stdin:       ClosedStdin,
			StdoutLimit: stdoutLimit,
			StderrLimit: CommandOutputLimit,
		})
		if runErr != nil {
			if ctx.Err() != nil {
				return VerifyResult{}, ErrCancelled
			}
			return VerifyResult{}, ErrIntegrity
		}
		if result.ExitCode != 0 {
			return VerifyResult{}, ErrIntegrity
		}
		switch args[0] {
		case "snapshots":
			if decodeResticSnapshotBinding(
				result.Stdout,
				input.SnapshotID,
				input.RunID,
				input.ManifestSHA256,
			) != nil {
				return VerifyResult{}, ErrIntegrity
			}
		case "stats":
			if decodeResticStats(result.Stdout) != nil {
				return VerifyResult{}, ErrIntegrity
			}
		case "dump":
			manifestBytes = append([]byte(nil), result.Stdout...)
		}
	}
	manifest, err := DecodeManifest(bytes.NewReader(manifestBytes))
	if err != nil {
		return VerifyResult{}, ErrIntegrity
	}
	actualHash := sha256.Sum256(manifestBytes)
	if manifest.BatchID != input.RunID ||
		!bytes.Equal(actualHash[:], input.ManifestSHA256[:]) {
		return VerifyResult{}, ErrIntegrity
	}
	return VerifyResult{Manifest: manifest}, nil
}

func (executor Executor) RemoteConfigured() (bool, error) {
	repository, repositoryErr := executor.config.Secrets.Read(SecretRemoteRepository)
	password, passwordErr := executor.config.Secrets.Read(SecretRemotePassword)
	if errors.Is(repositoryErr, ErrSecretUnavailable) &&
		errors.Is(passwordErr, ErrSecretUnavailable) {
		return false, nil
	}
	if repositoryErr != nil ||
		passwordErr != nil ||
		repository == "" ||
		password == "" {
		return false, ErrRemoteSync
	}
	return true, nil
}

func (executor Executor) Sync(
	ctx context.Context,
	runID string,
	snapshotIDs []string,
) (remoteSnapshotID string, resultErr error) {
	if !canonicalRunID(runID) || len(snapshotIDs) < 1 || len(snapshotIDs) > 2 {
		return "", ErrRemoteSync
	}
	seen := make(map[string]struct{}, len(snapshotIDs))
	for _, snapshotID := range snapshotIDs {
		if !resticSnapshotID.MatchString(snapshotID) {
			return "", ErrRemoteSync
		}
		if _, duplicate := seen[snapshotID]; duplicate {
			return "", ErrRemoteSync
		}
		seen[snapshotID] = struct{}{}
	}
	configured, err := executor.RemoteConfigured()
	if err != nil || !configured {
		return "", ErrRemoteSync
	}
	localRepository, err := executor.config.Secrets.Read(SecretLocalRepository)
	if err != nil {
		return "", ErrRemoteSync
	}
	localPassword, err := executor.config.Secrets.Read(SecretLocalPassword)
	if err != nil {
		return "", ErrRemoteSync
	}
	remoteRepository, err := executor.config.Secrets.Read(SecretRemoteRepository)
	if err != nil {
		return "", ErrRemoteSync
	}
	remotePassword, err := executor.config.Secrets.Read(SecretRemotePassword)
	if err != nil {
		return "", ErrRemoteSync
	}
	if err := secureDirectory(executor.config.WorkRoot); err != nil {
		return "", ErrRemoteSync
	}
	workDirectory, err := os.MkdirTemp(executor.config.WorkRoot, ".backup-sync-")
	if err != nil {
		return "", ErrRemoteSync
	}
	if err := os.Chmod(workDirectory, 0o700); err != nil {
		_ = os.RemoveAll(workDirectory)
		return "", ErrRemoteSync
	}
	defer func() {
		if err := cleanupWorkDirectory(executor.config.WorkRoot, workDirectory); err != nil {
			remoteSnapshotID = ""
			resultErr = ErrCleanup
		}
	}()
	localRepositoryFile := filepath.Join(workDirectory, "source-repository")
	localPasswordFile := filepath.Join(workDirectory, "source-password")
	if writeOwnerOnlyFile(localRepositoryFile, []byte(localRepository)) != nil ||
		writeOwnerOnlyFile(localPasswordFile, []byte(localPassword)) != nil {
		return "", ErrRemoteSync
	}
	args := []string{
		"copy",
		"--from-repository-file",
		localRepositoryFile,
		"--from-password-file",
		localPasswordFile,
	}
	args = append(args, snapshotIDs...)
	result, err := executor.config.Runner.Run(ctx, Command{
		Executable: resticExecutable,
		Args:       args,
		Env: []string{
			"LC_ALL=C",
			"RESTIC_REPOSITORY=" + remoteRepository,
			"RESTIC_PASSWORD=" + remotePassword,
		},
		Dir:         workDirectory,
		Stdin:       ClosedStdin,
		StdoutLimit: CommandOutputLimit,
		StderrLimit: CommandOutputLimit,
	})
	if err != nil {
		return "", mapExecutorRunError(ctx, err, ErrRemoteSync)
	}
	if result.ExitCode != 0 {
		return "", ErrRemoteSync
	}
	return snapshotIDs[len(snapshotIDs)-1], nil
}

type resticSummary struct {
	MessageType         string  `json:"message_type"`
	FilesNew            int64   `json:"files_new"`
	FilesChanged        int64   `json:"files_changed"`
	FilesUnmodified     int64   `json:"files_unmodified"`
	DirsNew             int64   `json:"dirs_new"`
	DirsChanged         int64   `json:"dirs_changed"`
	DirsUnmodified      int64   `json:"dirs_unmodified"`
	DataBlobs           int64   `json:"data_blobs"`
	TreeBlobs           int64   `json:"tree_blobs"`
	DataAdded           int64   `json:"data_added"`
	DataAddedPacked     int64   `json:"data_added_packed"`
	TotalFilesProcessed int64   `json:"total_files_processed"`
	TotalBytesProcessed int64   `json:"total_bytes_processed"`
	TotalDuration       float64 `json:"total_duration"`
	SnapshotID          string  `json:"snapshot_id"`
}

func decodeResticSummary(encoded []byte) (resticSummary, error) {
	if len(encoded) == 0 || len(encoded) > CommandOutputLimit {
		return resticSummary{}, ErrSnapshot
	}
	var summary resticSummary
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&summary); err != nil ||
		requireJSONEOF(decoder) != nil ||
		summary.MessageType != "summary" ||
		!resticSnapshotID.MatchString(summary.SnapshotID) ||
		summary.FilesNew < 0 ||
		summary.FilesChanged < 0 ||
		summary.FilesUnmodified < 0 ||
		summary.DirsNew < 0 ||
		summary.DirsChanged < 0 ||
		summary.DirsUnmodified < 0 ||
		summary.DataBlobs < 0 ||
		summary.TreeBlobs < 0 ||
		summary.DataAdded < 0 ||
		summary.DataAddedPacked < 0 ||
		summary.TotalFilesProcessed < 0 ||
		summary.TotalBytesProcessed < 0 ||
		summary.TotalDuration < 0 {
		return resticSummary{}, ErrSnapshot
	}
	return summary, nil
}

type resticStats struct {
	TotalSize      int64 `json:"total_size"`
	TotalFileCount int64 `json:"total_file_count"`
	SnapshotsCount int64 `json:"snapshots_count"`
}

type resticSnapshotBinding struct {
	Time           time.Time       `json:"time"`
	Parent         string          `json:"parent"`
	Tree           string          `json:"tree"`
	Paths          []string        `json:"paths"`
	Hostname       string          `json:"hostname"`
	Username       string          `json:"username"`
	UID            uint32          `json:"uid"`
	GID            uint32          `json:"gid"`
	Excludes       []string        `json:"excludes"`
	Tags           []string        `json:"tags"`
	ProgramVersion string          `json:"program_version"`
	Summary        json.RawMessage `json:"summary"`
	ID             string          `json:"id"`
	ShortID        string          `json:"short_id"`
	Original       string          `json:"original"`
}

func decodeResticSnapshotBinding(
	encoded []byte,
	snapshotID string,
	runID string,
	manifestHash [sha256.Size]byte,
) error {
	if len(encoded) == 0 || len(encoded) > CommandOutputLimit {
		return ErrIntegrity
	}
	var snapshots []resticSnapshotBinding
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshots); err != nil ||
		requireJSONEOF(decoder) != nil ||
		len(snapshots) != 1 ||
		snapshots[0].ID != snapshotID ||
		len(snapshots[0].Tags) != 2 {
		return ErrIntegrity
	}
	expectedTags := map[string]struct{}{
		"happylearn-batch:" + runID:                                         {},
		"happylearn-manifest-sha256:" + hex.EncodeToString(manifestHash[:]): {},
	}
	for _, tag := range snapshots[0].Tags {
		if _, ok := expectedTags[tag]; !ok {
			return ErrIntegrity
		}
		delete(expectedTags, tag)
	}
	if len(expectedTags) != 0 {
		return ErrIntegrity
	}
	return nil
}

func decodeResticStats(encoded []byte) error {
	if len(encoded) == 0 || len(encoded) > CommandOutputLimit {
		return ErrIntegrity
	}
	var stats resticStats
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stats); err != nil ||
		requireJSONEOF(decoder) != nil ||
		stats.TotalSize < 0 ||
		stats.TotalFileCount < 0 ||
		stats.SnapshotsCount != 1 {
		return ErrIntegrity
	}
	return nil
}

func mapExecutorRunError(ctx context.Context, err error, safe error) error {
	if ctx != nil && ctx.Err() != nil {
		return ErrCancelled
	}
	if errors.Is(err, ErrCommandOutputLimit) {
		return ErrCapacity
	}
	return safe
}

func canonicalRunID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func secureDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil ||
		!info.IsDir() ||
		info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 ||
		!ownedByCurrentUser(info) {
		return ErrExecutorConfig
	}
	return nil
}

func sourceDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil ||
		!info.IsDir() ||
		info.Mode()&os.ModeSymlink != 0 {
		return ErrExecutorConfig
	}
	return nil
}

func cleanupWorkDirectory(root string, workDirectory string) error {
	if filepath.Dir(filepath.Clean(workDirectory)) != filepath.Clean(root) {
		return ErrCleanup
	}
	return os.RemoveAll(workDirectory)
}

func summarizeObjectFiles(
	root string,
) (int64, int64, [sha256.Size]byte, error) {
	var count int64
	var bytesCount int64
	setHash := sha256.New()
	_, _ = setHash.Write([]byte("happylearn-object-set-v1\x00"))
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return ErrSnapshot
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrSnapshot
		}
		if entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 0 {
			return ErrSnapshot
		}
		if count == int64(^uint64(0)>>1) || bytesCount > int64(^uint64(0)>>1)-info.Size() {
			return ErrCapacity
		}
		relativePath, err := filepath.Rel(root, path)
		if err != nil ||
			relativePath == "." ||
			filepath.IsAbs(relativePath) ||
			strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
			return ErrSnapshot
		}
		file, err := os.Open(path)
		if err != nil {
			return ErrSnapshot
		}
		openedInfo, statErr := file.Stat()
		if statErr != nil || !os.SameFile(info, openedInfo) {
			_ = file.Close()
			return ErrSnapshot
		}
		fileHash := sha256.New()
		written, copyErr := io.Copy(fileHash, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || written != info.Size() {
			return ErrSnapshot
		}
		_, _ = setHash.Write([]byte(filepath.ToSlash(relativePath)))
		_, _ = setHash.Write([]byte{0})
		_, _ = setHash.Write([]byte(strconv.FormatInt(info.Size(), 10)))
		_, _ = setHash.Write([]byte{0})
		_, _ = setHash.Write(fileHash.Sum(nil))
		count++
		bytesCount += info.Size()
		return nil
	})
	if err != nil {
		return 0, 0, [sha256.Size]byte{}, err
	}
	var identity [sha256.Size]byte
	copy(identity[:], setHash.Sum(nil))
	return count, bytesCount, identity, nil
}

func hashBoundedRegularFile(path string, limit int64) ([sha256.Size]byte, int64, error) {
	var empty [sha256.Size]byte
	info, err := os.Lstat(path)
	if err != nil ||
		!info.Mode().IsRegular() ||
		info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 1 ||
		info.Size() > limit {
		return empty, 0, ErrCapacity
	}
	file, err := os.Open(path)
	if err != nil {
		return empty, 0, ErrDatabaseDump
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, limit+1))
	if err != nil || written != info.Size() || written > limit {
		return empty, 0, ErrCapacity
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result, written, nil
}

func writeOwnerOnlyFile(path string, contents []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrSnapshot
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return ErrSnapshot
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return ErrSnapshot
	}
	if err := file.Close(); err != nil {
		return ErrSnapshot
	}
	return nil
}
