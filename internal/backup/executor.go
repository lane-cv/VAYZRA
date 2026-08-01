package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
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
	SecretRemoteAccessKey  SecretName = "remote_access_key_id"
	SecretRemoteSecretKey  SecretName = "remote_secret_access_key"
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
		SecretRemoteRepository, SecretRemotePassword,
		SecretRemoteAccessKey, SecretRemoteSecretKey:
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
		directoryRootsOverlap(config.WorkRoot, config.ObjectRoot) ||
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

func directoryRootsOverlap(left string, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if directoryContains(left, right) || directoryContains(right, left) {
		return true
	}
	resolvedLeft, leftErr := filepath.EvalSymlinks(left)
	resolvedRight, rightErr := filepath.EvalSymlinks(right)
	return leftErr == nil &&
		rightErr == nil &&
		(directoryContains(resolvedLeft, resolvedRight) ||
			directoryContains(resolvedRight, resolvedLeft))
}

func directoryContains(root string, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative == "." ||
		(relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)))
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

type SyncInput struct {
	RunID            string
	SourceSnapshotID string
	ManifestSHA256   [sha256.Size]byte
}

type remoteSyncFailure struct {
	stage string
	cause error
}

func (failure *remoteSyncFailure) Error() string {
	return ErrRemoteSync.Error()
}

func (failure *remoteSyncFailure) Unwrap() []error {
	return []error{ErrRemoteSync, failure.cause}
}

func remoteSyncFailureAt(stage string, cause error) error {
	if cause == nil {
		cause = ErrRemoteSync
	}
	return &remoteSyncFailure{stage: stage, cause: cause}
}

func RemoteSyncFailureStage(err error) string {
	var failure *remoteSyncFailure
	if errors.As(err, &failure) {
		return failure.stage
	}
	return "unknown"
}

type snapshotFailure struct {
	stage       string
	cause       error
	exitCode    int
	hasExitCode bool
}

func (failure *snapshotFailure) Error() string {
	return failure.cause.Error()
}

func (failure *snapshotFailure) Unwrap() error {
	return failure.cause
}

func snapshotFailureAt(stage string, cause error) error {
	if !validSnapshotFailureStage(stage) {
		stage = "unknown"
	}
	return &snapshotFailure{
		stage: stage,
		cause: safeSnapshotFailureCause(cause),
	}
}

func snapshotExitFailureAt(stage string, cause error, exitCode int) error {
	failure := snapshotFailureAt(stage, cause).(*snapshotFailure)
	if !validSnapshotExitStage(failure.stage) {
		return failure
	}
	failure.exitCode = normalizeSnapshotExitCode(exitCode)
	failure.hasExitCode = true
	return failure
}

func normalizeSnapshotExitCode(exitCode int) int {
	if exitCode < 1 || exitCode > 255 {
		return -1
	}
	return exitCode
}

func validSnapshotExitStage(stage string) bool {
	switch stage {
	case "pg_dump_exit", "age_exit", "restic_exit":
		return true
	default:
		return false
	}
}

func safeSnapshotFailureCause(cause error) error {
	switch {
	case errors.Is(cause, ErrCancelled):
		return ErrCancelled
	case errors.Is(cause, ErrCleanup):
		return ErrCleanup
	case errors.Is(cause, ErrCapacity):
		return ErrCapacity
	case errors.Is(cause, ErrDatabaseDump):
		return ErrDatabaseDump
	case errors.Is(cause, ErrSnapshot):
		return ErrSnapshot
	default:
		return ErrSnapshot
	}
}

func validSnapshotFailureStage(stage string) bool {
	switch stage {
	case "input",
		"work_root",
		"object_root",
		"secrets",
		"work_dir",
		"pg_dump_run",
		"pg_dump_exit",
		"dump_hash",
		"object_walk",
		"object_symlink",
		"object_nonregular",
		"object_lstat",
		"object_open",
		"object_identity_changed",
		"object_read",
		"object_close",
		"object_size_changed",
		"object_capacity",
		"manifest",
		"recovery_bundle",
		"age_run",
		"age_exit",
		"restic_run",
		"restic_exit",
		"restic_summary",
		"capacity",
		"cleanup",
		"cancelled":
		return true
	default:
		return false
	}
}

func SnapshotFailureStage(err error) string {
	var failure *snapshotFailure
	if errors.As(err, &failure) &&
		failure != nil &&
		validSnapshotFailureStage(failure.stage) {
		return failure.stage
	}
	return "unknown"
}

func SnapshotFailureExitCode(err error) (int, bool) {
	var failure *snapshotFailure
	if !errors.As(err, &failure) ||
		failure == nil ||
		!failure.hasExitCode ||
		!validSnapshotExitStage(failure.stage) {
		return 0, false
	}
	return normalizeSnapshotExitCode(failure.exitCode), true
}

func (executor Executor) Snapshot(
	ctx context.Context,
	input SnapshotInput,
) (result SnapshotResult, resultErr error) {
	if !canonicalRunID(input.RunID) || input.DatabaseMigrationVersion < 1 {
		return SnapshotResult{}, snapshotFailureAt("input", ErrSnapshot)
	}
	if err := secureDirectory(executor.config.WorkRoot); err != nil {
		return SnapshotResult{}, snapshotFailureAt("work_root", ErrSnapshot)
	}
	if err := sourceDirectory(executor.config.ObjectRoot); err != nil {
		return SnapshotResult{}, snapshotFailureAt("object_root", ErrSnapshot)
	}
	if directoryRootsOverlap(
		executor.config.WorkRoot,
		executor.config.ObjectRoot,
	) {
		return SnapshotResult{}, snapshotFailureAt("object_root", ErrSnapshot)
	}
	databasePassword, err := executor.config.Secrets.Read(SecretDatabasePassword)
	if err != nil {
		return SnapshotResult{}, snapshotFailureAt("secrets", ErrSnapshot)
	}
	repository, err := executor.config.Secrets.Read(SecretLocalRepository)
	if err != nil {
		return SnapshotResult{}, snapshotFailureAt("secrets", ErrSnapshot)
	}
	repositoryPassword, err := executor.config.Secrets.Read(SecretLocalPassword)
	if err != nil {
		return SnapshotResult{}, snapshotFailureAt("secrets", ErrSnapshot)
	}
	workDirectory, err := os.MkdirTemp(executor.config.WorkRoot, ".backup-")
	if err != nil {
		return SnapshotResult{}, snapshotFailureAt("work_dir", ErrSnapshot)
	}
	if err := os.Chmod(workDirectory, 0o700); err != nil {
		_ = os.RemoveAll(workDirectory)
		return SnapshotResult{}, snapshotFailureAt("work_dir", ErrSnapshot)
	}
	defer func() {
		if err := cleanupWorkDirectory(executor.config.WorkRoot, workDirectory); err != nil {
			result = SnapshotResult{}
			resultErr = snapshotFailureAt("cleanup", ErrCleanup)
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
		mapped := mapExecutorRunError(ctx, err, ErrDatabaseDump)
		if errors.Is(mapped, ErrCancelled) {
			return SnapshotResult{}, snapshotFailureAt("cancelled", mapped)
		}
		return SnapshotResult{}, snapshotFailureAt("pg_dump_run", mapped)
	}
	if pgResult.ExitCode != 0 {
		return SnapshotResult{}, snapshotExitFailureAt(
			"pg_dump_exit",
			ErrDatabaseDump,
			pgResult.ExitCode,
		)
	}
	dumpHash, dumpBytes, err := hashBoundedRegularFile(
		ctx,
		dumpPath,
		int64(executor.config.MaxPlaintextBytes),
	)
	if err != nil {
		if errors.Is(err, ErrCancelled) {
			return SnapshotResult{}, snapshotFailureAt("cancelled", err)
		}
		return SnapshotResult{}, snapshotFailureAt("dump_hash", ErrDatabaseDump)
	}

	objectCount, referencedBytes, objectIdentity, err :=
		summarizeObjectFiles(ctx, executor.config.ObjectRoot)
	if err != nil {
		return SnapshotResult{}, snapshotObjectSummaryFailure(err)
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
		return SnapshotResult{}, snapshotFailureAt("manifest", ErrSnapshot)
	}
	manifestHash := sha256.Sum256(manifestBytes)
	manifestPath := filepath.Join(workDirectory, "manifest.json")
	if err := writeOwnerOnlyFile(manifestPath, manifestBytes); err != nil {
		return SnapshotResult{}, snapshotFailureAt("manifest", ErrSnapshot)
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
		return SnapshotResult{}, snapshotFailureAt(
			"recovery_bundle",
			ErrSnapshot,
		)
	}
	recoveryBundlePath := filepath.Join(workDirectory, "recovery-bundle.json")
	if err := writeOwnerOnlyFile(recoveryBundlePath, recoveryBundleBytes); err != nil {
		return SnapshotResult{}, snapshotFailureAt(
			"recovery_bundle",
			ErrSnapshot,
		)
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
		mapped := mapExecutorRunError(ctx, err, ErrSnapshot)
		if errors.Is(mapped, ErrCancelled) {
			return SnapshotResult{}, snapshotFailureAt("cancelled", mapped)
		}
		return SnapshotResult{}, snapshotFailureAt("age_run", mapped)
	}
	if ageResult.ExitCode != 0 {
		return SnapshotResult{}, snapshotExitFailureAt(
			"age_exit",
			ErrSnapshot,
			ageResult.ExitCode,
		)
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
		return SnapshotResult{}, snapshotFailureAt("capacity", ErrCapacity)
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
		"--no-cache",
		"backup",
		"--json",
		"--quiet",
	}
	for _, tag := range tags {
		if tag == "" || strings.TrimSpace(tag) != tag {
			return resticSummary{}, snapshotFailureAt(
				"restic_run",
				ErrSnapshot,
			)
		}
		args = append(args, "--tag", tag)
	}
	args = append(args, paths...)
	result, err := executor.config.Runner.Run(ctx, Command{
		Executable: resticExecutable,
		Args:       args,
		Env: []string{
			"LC_ALL=C",
			"TMPDIR=" + workDirectory,
			"RESTIC_REPOSITORY=" + repository,
			"RESTIC_PASSWORD=" + password,
		},
		Dir:         workDirectory,
		Stdin:       ClosedStdin,
		StdoutLimit: CommandOutputLimit,
		StderrLimit: CommandOutputLimit,
	})
	if err != nil {
		mapped := mapExecutorRunError(ctx, err, ErrSnapshot)
		if errors.Is(mapped, ErrCancelled) {
			return resticSummary{}, snapshotFailureAt("cancelled", mapped)
		}
		return resticSummary{}, snapshotFailureAt("restic_run", mapped)
	}
	if result.ExitCode != 0 {
		return resticSummary{}, snapshotExitFailureAt(
			"restic_exit",
			ErrSnapshot,
			result.ExitCode,
		)
	}
	summary, err := decodeResticSummary(result.Stdout)
	if err != nil {
		return resticSummary{}, snapshotFailureAt(
			"restic_summary",
			ErrSnapshot,
		)
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
	if err := secureDirectory(executor.config.WorkRoot); err != nil {
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
	for _, operationArgs := range [][]string{
		{"check", "--read-data"},
		{"snapshots", "--json", input.SnapshotID},
		{"stats", "--json", "--mode=raw-data", input.SnapshotID},
		{"dump", input.SnapshotID, "manifest.json"},
	} {
		operation := operationArgs[0]
		args := append([]string{"--no-cache"}, operationArgs...)
		stdoutLimit := CommandOutputLimit
		if operation == "dump" {
			stdoutLimit = ManifestMaxBytes
		}
		result, runErr := executor.config.Runner.Run(ctx, Command{
			Executable: resticExecutable,
			Args:       args,
			Env: []string{
				"LC_ALL=C",
				"TMPDIR=" + executor.config.WorkRoot,
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
		switch operation {
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
	_, configured, err := executor.remoteConfiguration()
	return configured, err
}

func (executor Executor) LocalConfigured() (bool, error) {
	for _, name := range []SecretName{
		SecretLocalRepository,
		SecretLocalPassword,
	} {
		value, err := executor.config.Secrets.Read(name)
		if err != nil || value == "" {
			return false, ErrSnapshot
		}
	}
	return true, nil
}

type remoteConfiguration struct {
	repository string
	password   string
	accessKey  string
	secretKey  string
}

func (executor Executor) remoteConfiguration() (remoteConfiguration, bool, error) {
	values := make(map[SecretName]string, 4)
	missing := 0
	for _, name := range []SecretName{
		SecretRemoteRepository,
		SecretRemotePassword,
		SecretRemoteAccessKey,
		SecretRemoteSecretKey,
	} {
		value, err := executor.config.Secrets.Read(name)
		if errors.Is(err, ErrSecretUnavailable) {
			missing++
			continue
		}
		if err != nil || value == "" {
			return remoteConfiguration{}, false, ErrRemoteSync
		}
		values[name] = value
	}
	if missing == 4 {
		return remoteConfiguration{}, false, nil
	}
	if missing != 0 || !validRemoteRepository(values[SecretRemoteRepository]) {
		return remoteConfiguration{}, false, ErrRemoteSync
	}
	return remoteConfiguration{
		repository: values[SecretRemoteRepository],
		password:   values[SecretRemotePassword],
		accessKey:  values[SecretRemoteAccessKey],
		secretKey:  values[SecretRemoteSecretKey],
	}, true, nil
}

func validRemoteRepository(repository string) bool {
	const prefix = "s3:"
	if !strings.HasPrefix(repository, prefix) ||
		strings.TrimSpace(repository) != repository ||
		strings.ContainsAny(repository, "\x00\r\n") {
		return false
	}
	location, err := url.Parse(strings.TrimPrefix(repository, prefix))
	if err != nil ||
		location.Scheme != "https" ||
		location.Host == "" ||
		location.User != nil ||
		location.RawQuery != "" ||
		location.Fragment != "" ||
		location.Opaque != "" {
		return false
	}
	bucketPath := strings.Trim(location.EscapedPath(), "/")
	return bucketPath != "" &&
		!strings.Contains(bucketPath, `\`) &&
		!strings.Contains(strings.ToLower(bucketPath), "insecure-tls")
}

func (executor Executor) Sync(
	ctx context.Context,
	input SyncInput,
) (remoteSnapshotID string, resultErr error) {
	if !canonicalRunID(input.RunID) ||
		!resticSnapshotID.MatchString(input.SourceSnapshotID) ||
		len(input.SourceSnapshotID) != sha256.Size*2 ||
		input.ManifestSHA256 == [sha256.Size]byte{} {
		return "", remoteSyncFailureAt("input", ErrRemoteSync)
	}
	remote, configured, err := executor.remoteConfiguration()
	if err != nil || !configured {
		return "", remoteSyncFailureAt("configuration", err)
	}
	localRepository, err := executor.config.Secrets.Read(SecretLocalRepository)
	if err != nil {
		return "", remoteSyncFailureAt("configuration", err)
	}
	localPassword, err := executor.config.Secrets.Read(SecretLocalPassword)
	if err != nil {
		return "", remoteSyncFailureAt("configuration", err)
	}
	if err := secureDirectory(executor.config.WorkRoot); err != nil {
		return "", remoteSyncFailureAt("work_setup", err)
	}
	workDirectory, err := os.MkdirTemp(executor.config.WorkRoot, ".backup-sync-")
	if err != nil {
		return "", remoteSyncFailureAt("work_setup", err)
	}
	if err := os.Chmod(workDirectory, 0o700); err != nil {
		_ = os.RemoveAll(workDirectory)
		return "", remoteSyncFailureAt("work_setup", err)
	}
	defer func() {
		if err := cleanupWorkDirectory(executor.config.WorkRoot, workDirectory); err != nil {
			remoteSnapshotID = ""
			resultErr = remoteSyncFailureAt("cleanup", ErrCleanup)
		}
	}()
	localRepositoryFile := filepath.Join(workDirectory, "source-repository")
	localPasswordFile := filepath.Join(workDirectory, "source-password")
	if writeOwnerOnlyFile(localRepositoryFile, []byte(localRepository)) != nil ||
		writeOwnerOnlyFile(localPasswordFile, []byte(localPassword)) != nil {
		return "", remoteSyncFailureAt("work_setup", ErrRemoteSync)
	}
	remoteEnv := []string{
		"LC_ALL=C",
		"TMPDIR=" + workDirectory,
		"RESTIC_REPOSITORY=" + remote.repository,
		"RESTIC_PASSWORD=" + remote.password,
		"AWS_ACCESS_KEY_ID=" + remote.accessKey,
		"AWS_SECRET_ACCESS_KEY=" + remote.secretKey,
	}
	copyResult, err := executor.config.Runner.Run(ctx, Command{
		Executable: resticExecutable,
		Args: []string{
			"--no-cache",
			"copy",
			"--from-repository-file",
			localRepositoryFile,
			"--from-password-file",
			localPasswordFile,
			input.SourceSnapshotID,
		},
		Env:         remoteEnv,
		Dir:         workDirectory,
		Stdin:       ClosedStdin,
		StdoutLimit: CommandOutputLimit,
		StderrLimit: CommandOutputLimit,
	})
	if err != nil {
		return "", remoteSyncFailureAt(
			"copy",
			mapExecutorRunError(ctx, err, ErrRemoteSync),
		)
	}
	if copyResult.ExitCode != 0 {
		return "", remoteSyncFailureAt("copy", ErrRemoteSync)
	}
	batchTag := "happylearn-batch:" + input.RunID
	manifestTag := "happylearn-manifest-sha256:" +
		hex.EncodeToString(input.ManifestSHA256[:])
	lookupResult, err := executor.runRemoteRestic(
		ctx,
		remoteEnv,
		workDirectory,
		CommandOutputLimit,
		"snapshots",
		"--json",
		"--tag",
		batchTag,
		"--tag",
		manifestTag,
	)
	if err != nil {
		return "", remoteSyncFailureAt("snapshot_lookup", err)
	}
	destinationID, err := decodeResticCopiedSnapshot(
		lookupResult,
		input.SourceSnapshotID,
		input.RunID,
		input.ManifestSHA256,
	)
	if err != nil {
		return "", remoteSyncFailureAt("snapshot_lookup", err)
	}
	for _, verification := range []struct {
		limit int
		args  []string
	}{
		{CommandOutputLimit, []string{"check", "--read-data"}},
		{CommandOutputLimit, []string{"stats", "--json", "--mode=raw-data", destinationID}},
		{ManifestMaxBytes, []string{"dump", destinationID, "manifest.json"}},
	} {
		result, runErr := executor.runRemoteRestic(
			ctx,
			remoteEnv,
			workDirectory,
			verification.limit,
			verification.args...,
		)
		if runErr != nil {
			switch verification.args[0] {
			case "check":
				return "", remoteSyncFailureAt("repository_check", runErr)
			case "stats":
				return "", remoteSyncFailureAt("snapshot_stats", runErr)
			default:
				return "", remoteSyncFailureAt("manifest_check", runErr)
			}
		}
		switch verification.args[0] {
		case "stats":
			if decodeResticStats(result) != nil {
				return "", remoteSyncFailureAt("snapshot_stats", ErrRemoteSync)
			}
		case "dump":
			manifest, decodeErr := DecodeManifest(bytes.NewReader(result))
			actualHash := sha256.Sum256(result)
			if decodeErr != nil ||
				manifest.BatchID != input.RunID ||
				!bytes.Equal(actualHash[:], input.ManifestSHA256[:]) {
				return "", remoteSyncFailureAt("manifest_check", ErrRemoteSync)
			}
		}
	}
	return destinationID, nil
}

func (executor Executor) runRemoteRestic(
	ctx context.Context,
	env []string,
	workDirectory string,
	stdoutLimit int,
	args ...string,
) ([]byte, error) {
	result, err := executor.config.Runner.Run(ctx, Command{
		Executable:  resticExecutable,
		Args:        append([]string{"--no-cache"}, args...),
		Env:         append([]string(nil), env...),
		Dir:         workDirectory,
		Stdin:       ClosedStdin,
		StdoutLimit: stdoutLimit,
		StderrLimit: CommandOutputLimit,
	})
	if err != nil {
		return nil, mapExecutorRunError(ctx, err, ErrRemoteSync)
	}
	if result.ExitCode != 0 {
		return nil, ErrRemoteSync
	}
	return append([]byte(nil), result.Stdout...), nil
}

func decodeResticCopiedSnapshot(
	encoded []byte,
	sourceSnapshotID string,
	runID string,
	manifestHash [sha256.Size]byte,
) (string, error) {
	var snapshots []resticSnapshotBinding
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if len(encoded) == 0 ||
		len(encoded) > CommandOutputLimit ||
		decoder.Decode(&snapshots) != nil ||
		requireJSONEOF(decoder) != nil ||
		len(snapshots) != 1 {
		return "", ErrRemoteSync
	}
	snapshot := snapshots[0]
	if len(snapshot.ID) != sha256.Size*2 ||
		!resticSnapshotID.MatchString(snapshot.ID) ||
		snapshot.ID == sourceSnapshotID ||
		snapshot.Original != sourceSnapshotID ||
		!exactSnapshotTags(snapshot.Tags, runID, manifestHash) {
		return "", ErrRemoteSync
	}
	return snapshot.ID, nil
}

func exactSnapshotTags(
	tags []string,
	runID string,
	manifestHash [sha256.Size]byte,
) bool {
	if len(tags) != 2 {
		return false
	}
	expected := map[string]struct{}{
		"happylearn-batch:" + runID:                                         {},
		"happylearn-manifest-sha256:" + hex.EncodeToString(manifestHash[:]): {},
	}
	for _, tag := range tags {
		if _, ok := expected[tag]; !ok {
			return false
		}
		delete(expected, tag)
	}
	return len(expected) == 0
}

type resticSummary struct {
	MessageType         string    `json:"message_type"`
	FilesNew            int64     `json:"files_new"`
	FilesChanged        int64     `json:"files_changed"`
	FilesUnmodified     int64     `json:"files_unmodified"`
	DirsNew             int64     `json:"dirs_new"`
	DirsChanged         int64     `json:"dirs_changed"`
	DirsUnmodified      int64     `json:"dirs_unmodified"`
	DataBlobs           int64     `json:"data_blobs"`
	TreeBlobs           int64     `json:"tree_blobs"`
	DataAdded           int64     `json:"data_added"`
	DataAddedPacked     int64     `json:"data_added_packed"`
	TotalFilesProcessed int64     `json:"total_files_processed"`
	TotalBytesProcessed int64     `json:"total_bytes_processed"`
	TotalDuration       float64   `json:"total_duration"`
	BackupStart         time.Time `json:"backup_start"`
	BackupEnd           time.Time `json:"backup_end"`
	SnapshotID          string    `json:"snapshot_id"`
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
		summary.TotalDuration < 0 ||
		summary.BackupStart.IsZero() != summary.BackupEnd.IsZero() ||
		(!summary.BackupStart.IsZero() &&
			summary.BackupEnd.Before(summary.BackupStart)) {
		return resticSummary{}, ErrSnapshot
	}
	return summary, nil
}

type resticStats struct {
	TotalSize              *int64   `json:"total_size"`
	TotalUncompressedSize  *int64   `json:"total_uncompressed_size"`
	CompressionRatio       *float64 `json:"compression_ratio"`
	CompressionProgress    *float64 `json:"compression_progress"`
	CompressionSpaceSaving *float64 `json:"compression_space_saving"`
	TotalBlobCount         *int64   `json:"total_blob_count"`
	SnapshotsCount         *int64   `json:"snapshots_count"`
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
		stats.TotalSize == nil ||
		stats.TotalUncompressedSize == nil ||
		stats.CompressionRatio == nil ||
		stats.CompressionProgress == nil ||
		stats.CompressionSpaceSaving == nil ||
		stats.TotalBlobCount == nil ||
		stats.SnapshotsCount == nil ||
		*stats.TotalSize < 0 ||
		*stats.TotalUncompressedSize < 0 ||
		*stats.TotalBlobCount < 0 ||
		*stats.SnapshotsCount != 1 ||
		!finiteNonnegative(*stats.CompressionRatio) ||
		!finiteRange(*stats.CompressionProgress, 0, 100) ||
		math.IsNaN(*stats.CompressionSpaceSaving) ||
		math.IsInf(*stats.CompressionSpaceSaving, 0) ||
		*stats.CompressionSpaceSaving > 100 {
		return ErrIntegrity
	}
	return nil
}

func finiteNonnegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func finiteRange(value, minimum, maximum float64) bool {
	return value >= minimum &&
		value <= maximum &&
		!math.IsNaN(value) &&
		!math.IsInf(value, 0)
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
	ctx context.Context,
	root string,
) (int64, int64, [sha256.Size]byte, error) {
	if ctx == nil {
		return 0, 0, [sha256.Size]byte{},
			snapshotFailureAt("cancelled", ErrCancelled)
	}
	var count int64
	var bytesCount int64
	setHash := sha256.New()
	_, _ = setHash.Write([]byte("happylearn-object-set-v1\x00"))
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return snapshotFailureAt("cancelled", ErrCancelled)
		}
		if walkErr != nil {
			return snapshotFailureAt("object_walk", ErrSnapshot)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return snapshotFailureAt("object_symlink", ErrSnapshot)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return snapshotFailureAt("object_lstat", ErrSnapshot)
		}
		if !info.Mode().IsRegular() || info.Size() < 0 {
			return snapshotFailureAt("object_nonregular", ErrSnapshot)
		}
		if count == int64(^uint64(0)>>1) || bytesCount > int64(^uint64(0)>>1)-info.Size() {
			return snapshotFailureAt("object_capacity", ErrCapacity)
		}
		relativePath, err := filepath.Rel(root, path)
		if err != nil ||
			relativePath == "." ||
			filepath.IsAbs(relativePath) ||
			strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
			return snapshotFailureAt("object_walk", ErrSnapshot)
		}
		file, err := os.Open(path)
		if err != nil {
			return snapshotFailureAt("object_open", ErrSnapshot)
		}
		openedInfo, statErr := file.Stat()
		if statErr != nil || !os.SameFile(info, openedInfo) {
			_ = file.Close()
			return snapshotFailureAt(
				"object_identity_changed",
				ErrSnapshot,
			)
		}
		fileHash, written, copyErr := hashFileContents(
			ctx,
			file,
			info.Size(),
		)
		closeErr := file.Close()
		if failure := objectContentFailure(
			copyErr,
			closeErr,
			written,
			info.Size(),
		); failure != nil {
			return failure
		}
		_, _ = setHash.Write([]byte(filepath.ToSlash(relativePath)))
		_, _ = setHash.Write([]byte{0})
		_, _ = setHash.Write([]byte(strconv.FormatInt(info.Size(), 10)))
		_, _ = setHash.Write([]byte{0})
		_, _ = setHash.Write(fileHash[:])
		count++
		bytesCount += info.Size()
		return nil
	})
	if err != nil {
		if SnapshotFailureStage(err) == "unknown" {
			err = snapshotFailureAt("object_walk", err)
		}
		return 0, 0, [sha256.Size]byte{}, err
	}
	var identity [sha256.Size]byte
	copy(identity[:], setHash.Sum(nil))
	return count, bytesCount, identity, nil
}

func snapshotObjectSummaryFailure(err error) error {
	if errors.Is(err, ErrCancelled) {
		return snapshotFailureAt("cancelled", ErrCancelled)
	}
	stage := SnapshotFailureStage(err)
	if stage == "unknown" {
		stage = "object_walk"
	}
	return snapshotFailureAt(stage, ErrSnapshot)
}

func objectContentFailure(
	copyErr error,
	closeErr error,
	written int64,
	expected int64,
) error {
	switch {
	case errors.Is(copyErr, ErrCancelled):
		return snapshotFailureAt("cancelled", ErrCancelled)
	case errors.Is(copyErr, ErrCapacity):
		return snapshotFailureAt("object_size_changed", ErrSnapshot)
	case copyErr != nil:
		return snapshotFailureAt("object_read", ErrSnapshot)
	case closeErr != nil:
		return snapshotFailureAt("object_close", ErrSnapshot)
	case written != expected:
		return snapshotFailureAt("object_size_changed", ErrSnapshot)
	default:
		return nil
	}
}

func hashBoundedRegularFile(
	ctx context.Context,
	path string,
	limit int64,
) ([sha256.Size]byte, int64, error) {
	var empty [sha256.Size]byte
	if ctx == nil || ctx.Err() != nil {
		return empty, 0, ErrCancelled
	}
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
	hash, written, readErr := hashFileContents(ctx, file, limit)
	closeErr := file.Close()
	if errors.Is(readErr, ErrCancelled) {
		return empty, 0, ErrCancelled
	}
	if readErr != nil ||
		closeErr != nil ||
		written != info.Size() ||
		written > limit {
		return empty, 0, ErrCapacity
	}
	return hash, written, nil
}

func hashFileContents(
	ctx context.Context,
	file *os.File,
	limit int64,
) ([sha256.Size]byte, int64, error) {
	var empty [sha256.Size]byte
	if ctx == nil || file == nil || limit < 0 {
		return empty, 0, ErrCapacity
	}
	hasher := sha256.New()
	buffer := make([]byte, 64*1024)
	var written int64
	for {
		if ctx.Err() != nil {
			return empty, written, ErrCancelled
		}
		count, err := file.Read(buffer)
		if count > 0 {
			if written > limit-int64(count) {
				return empty, written, ErrCapacity
			}
			_, _ = hasher.Write(buffer[:count])
			written += int64(count)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return empty, written, err
		}
	}
	var result [sha256.Size]byte
	copy(result[:], hasher.Sum(nil))
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
