//go:build unix

package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type recordingRunner struct {
	mu       sync.Mutex
	commands []Command
	run      func(context.Context, Command, int) (CommandResult, error)
}

func (r *recordingRunner) Run(ctx context.Context, command Command) (CommandResult, error) {
	r.mu.Lock()
	index := len(r.commands)
	r.commands = append(r.commands, command)
	r.mu.Unlock()
	if r.run != nil {
		return r.run(ctx, command, index)
	}
	return CommandResult{}, nil
}

func (r *recordingRunner) calls() []Command {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.commands)
}

type mapSecrets map[SecretName]string

func (s mapSecrets) Read(name SecretName) (string, error) {
	value, ok := s[name]
	if !ok {
		return "", ErrSecretUnavailable
	}
	return value, nil
}

func executorFixture(t *testing.T, runner Runner) (Executor, string) {
	t.Helper()
	workRoot := t.TempDir()
	objectRoot := t.TempDir()
	if err := os.Chmod(workRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(objectRoot, "object.bin"), []byte("object-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorConfig{
		Runner: runner,
		Secrets: mapSecrets{
			SecretDatabasePassword: "database-password-secret",
			SecretLocalRepository:  "/private/local/repository",
			SecretLocalPassword:    "repository-password-secret",
		},
		WorkRoot:        workRoot,
		ObjectRoot:      objectRoot,
		DatabaseHost:    "postgres",
		DatabasePort:    "5432",
		DatabaseUser:    "happylearn",
		DatabaseName:    "happylearn",
		DatabaseSSLMode: "require",
		AgeRecipient:    "age1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqp5m40h",
		EncryptionKeyID: "key-2026-07",
		Now: func() time.Time {
			return time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)
		},
		MaxPlaintextBytes: 16 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor, workRoot
}

func TestExecutorSnapshotKeepsSecretsOutOfArgumentsAndCleansPlaintext(t *testing.T) {
	runner := &recordingRunner{}
	runner.run = func(_ context.Context, command Command, index int) (CommandResult, error) {
		switch filepath.Base(command.Executable) {
		case "pg_dump":
			if command.StdoutFile == "" {
				t.Fatal("pg_dump missing bounded stdout file")
			}
			if err := os.WriteFile(command.StdoutFile, []byte("PGDMP plaintext fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			return CommandResult{ExitCode: 0}, nil
		case "restic":
			snapshot := "1111111111111111"
			if index > 1 {
				snapshot = "2222222222222222"
			}
			return CommandResult{
				Stdout: []byte(fmt.Sprintf(
					`{"message_type":"summary","files_new":1,"files_changed":0,"files_unmodified":0,"dirs_new":0,"dirs_changed":0,"dirs_unmodified":0,"data_blobs":1,"tree_blobs":1,"data_added":128,"data_added_packed":96,"total_files_processed":1,"total_bytes_processed":22,"total_duration":0.1,"snapshot_id":%q}`,
					snapshot,
				)),
				ExitCode: 0,
			}, nil
		case "age":
			if command.StdoutFile == "" {
				t.Fatal("age missing output file")
			}
			if len(command.Args) == 0 {
				t.Fatal("age missing owner-only recovery bundle input")
			}
			bundle, err := os.ReadFile(command.Args[len(command.Args)-1])
			if err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{
				`"repository":"/private/local/repository"`,
				`"manifest":{`,
				`"instructions":["restic check","restic restore","pg_restore"]`,
			} {
				if !strings.Contains(string(bundle), required) {
					t.Fatalf("recovery bundle missing %q", required)
				}
			}
			if err := os.WriteFile(command.StdoutFile, []byte("age-encrypted-bundle"), 0o600); err != nil {
				t.Fatal(err)
			}
			return CommandResult{ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected executable %q", command.Executable)
			return CommandResult{}, nil
		}
	}
	executor, workRoot := executorFixture(t, runner)

	result, err := executor.Snapshot(context.Background(), SnapshotInput{
		RunID:                    "10000000-0000-4000-8000-000000000001",
		DatabaseMigrationVersion: 20,
	})
	if err != nil {
		calls := runner.calls()
		executables := make([]string, 0, len(calls))
		for _, call := range calls {
			executables = append(executables, filepath.Base(call.Executable))
		}
		t.Fatalf("err=%v executables=%v", err, executables)
	}
	if result.LocalSnapshotID != "2222222222222222" ||
		result.Manifest.ObjectSnapshotID != "1111111111111111" ||
		result.ManifestSHA256 == [32]byte{} {
		t.Fatalf("result=%+v", result)
	}
	for _, command := range runner.calls() {
		if command.Stdin != ClosedStdin {
			t.Errorf("stdin=%q for %s", command.Stdin, command.Executable)
		}
		joined := strings.Join(command.Args, "\x00")
		for _, forbidden := range []string{
			"database-password-secret",
			"repository-password-secret",
			"/private/local/repository",
			"postgres://",
			"PGPASSWORD",
		} {
			if strings.Contains(joined, forbidden) {
				t.Errorf("secret/path %q in argv for %s", forbidden, command.Executable)
			}
		}
		if command.StdoutLimit < 1 || command.StderrLimit != CommandOutputLimit {
			t.Errorf("unbounded command=%+v", command)
		}
	}
	entries, err := os.ReadDir(workRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary plaintext remains: %v", entries)
	}
}

func TestExecutorCleansPlaintextOnFailureAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(context.Context, Command, int) (CommandResult, error)
		call func(context.Context, Executor) error
	}{
		{
			name: "failure",
			run: func(_ context.Context, command Command, _ int) (CommandResult, error) {
				if command.StdoutFile != "" {
					_ = os.WriteFile(command.StdoutFile, []byte("PGDMP secret"), 0o600)
				}
				return CommandResult{ExitCode: 1, Stderr: []byte("repository-password-secret /private/local/repository")}, nil
			},
			call: func(ctx context.Context, executor Executor) error {
				_, err := executor.Snapshot(ctx, SnapshotInput{
					RunID:                    "10000000-0000-4000-8000-000000000001",
					DatabaseMigrationVersion: 20,
				})
				return err
			},
		},
		{
			name: "cancellation",
			run: func(ctx context.Context, command Command, _ int) (CommandResult, error) {
				if command.StdoutFile != "" {
					_ = os.WriteFile(command.StdoutFile, []byte("PGDMP secret"), 0o600)
				}
				<-ctx.Done()
				return CommandResult{}, ctx.Err()
			},
			call: func(_ context.Context, executor Executor) error {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				_, err := executor.Snapshot(ctx, SnapshotInput{
					RunID:                    "10000000-0000-4000-8000-000000000001",
					DatabaseMigrationVersion: 20,
				})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &recordingRunner{run: tc.run}
			executor, workRoot := executorFixture(t, runner)
			err := tc.call(context.Background(), executor)
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "repository-password-secret") ||
				strings.Contains(err.Error(), "/private/local/repository") ||
				strings.Contains(err.Error(), "PGDMP") {
				t.Fatalf("sensitive error=%v", err)
			}
			entries, readErr := os.ReadDir(workRoot)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("temporary plaintext remains: %v", entries)
			}
		})
	}
}

func TestExecutorMapsWrongOrTamperedSnapshotToSafeIntegrityError(t *testing.T) {
	runner := &recordingRunner{run: func(_ context.Context, command Command, _ int) (CommandResult, error) {
		if len(command.Args) < 2 ||
			command.Args[0] != "check" ||
			command.Args[1] != "--read-data" {
			t.Fatalf("integrity command=%q", command.Args)
		}
		return CommandResult{
			ExitCode: 1,
			Stderr:   []byte("wrong password repository=/private/local/repository pack=secret"),
		}, nil
	}}
	executor, _ := executorFixture(t, runner)
	err := executor.Verify(
		context.Background(),
		"10000000-0000-4000-8000-000000000001",
		"1111111111111111",
	)
	if !errors.Is(err, ErrIntegrity) || err.Error() != ErrIntegrity.Error() {
		t.Fatalf("err=%v", err)
	}
}

func TestExecutorSyncUsesOwnerOnlySourceFilesAndKeepsRepositoriesOutOfArguments(t *testing.T) {
	runner := &recordingRunner{run: func(_ context.Context, command Command, _ int) (CommandResult, error) {
		joined := strings.Join(command.Args, "\x00")
		for _, forbidden := range []string{
			"/private/local/repository",
			"/private/remote/repository",
			"local-password-secret",
			"remote-password-secret",
		} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("sync argv leaked %q", forbidden)
			}
		}
		if len(command.Args) < 1 || command.Args[0] != "copy" {
			t.Fatalf("args=%q", command.Args)
		}
		return CommandResult{ExitCode: 0}, nil
	}}
	executor, workRoot := executorFixture(t, runner)
	executor.config.Secrets = mapSecrets{
		SecretDatabasePassword: "database-password-secret",
		SecretLocalRepository:  "/private/local/repository",
		SecretLocalPassword:    "local-password-secret",
		SecretRemoteRepository: "/private/remote/repository",
		SecretRemotePassword:   "remote-password-secret",
	}
	configured, err := executor.RemoteConfigured()
	if err != nil || !configured {
		t.Fatalf("configured=%v err=%v", configured, err)
	}
	remoteSnapshotID, err := executor.Sync(
		context.Background(),
		commandRunIDForExecutor,
		[]string{"1111111111111111", "2222222222222222"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if remoteSnapshotID != "2222222222222222" {
		t.Fatalf("remoteSnapshotID=%q", remoteSnapshotID)
	}
	entries, err := os.ReadDir(workRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("sync source files remain: %v", entries)
	}
}

const commandRunIDForExecutor = "10000000-0000-4000-8000-000000000001"

func TestExecutorRejectsPartialRemoteConfiguration(t *testing.T) {
	executor, _ := executorFixture(t, &recordingRunner{})
	executor.config.Secrets = mapSecrets{
		SecretDatabasePassword: "database-password-secret",
		SecretLocalRepository:  "/private/local/repository",
		SecretLocalPassword:    "local-password-secret",
		SecretRemoteRepository: "/private/remote/repository",
	}
	if configured, err := executor.RemoteConfigured(); configured || !errors.Is(err, ErrRemoteSync) {
		t.Fatalf("configured=%v err=%v", configured, err)
	}
}

func TestExecRunnerBoundsOutputClosesStdinAndCancelsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix process group contract")
	}
	runner := ExecRunner{}
	t.Run("bounded output", func(t *testing.T) {
		result, err := runner.Run(context.Background(), Command{
			Executable:  os.Args[0],
			Args:        []string{"-test.run=TestExecutorHelperProcess", "--", "output"},
			Env:         []string{"GO_WANT_BACKUP_HELPER=1"},
			Stdin:       ClosedStdin,
			StdoutLimit: CommandOutputLimit,
			StderrLimit: CommandOutputLimit,
		})
		if !errors.Is(err, ErrCommandOutputLimit) ||
			len(result.Stdout) != CommandOutputLimit {
			t.Fatalf("bytes=%d err=%v", len(result.Stdout), err)
		}
	})
	t.Run("closed stdin", func(t *testing.T) {
		_, err := runner.Run(context.Background(), Command{
			Executable:  os.Args[0],
			Args:        []string{"-test.run=TestExecutorHelperProcess", "--", "stdin"},
			Env:         []string{"GO_WANT_BACKUP_HELPER=1"},
			Stdin:       ClosedStdin,
			StdoutLimit: CommandOutputLimit,
			StderrLimit: CommandOutputLimit,
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("process group cancellation", func(t *testing.T) {
		pidFile := filepath.Join(t.TempDir(), "child.pid")
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		started := time.Now()
		_, err := runner.Run(ctx, Command{
			Executable:  os.Args[0],
			Args:        []string{"-test.run=TestExecutorHelperProcess", "--", "cancel", pidFile},
			Env:         []string{"GO_WANT_BACKUP_HELPER=1"},
			Stdin:       ClosedStdin,
			StdoutLimit: CommandOutputLimit,
			StderrLimit: CommandOutputLimit,
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
		if time.Since(started) > 3*time.Second {
			t.Fatalf("cancellation took %v", time.Since(started))
		}
		data, readErr := os.ReadFile(pidFile)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var pid int
		if _, scanErr := fmt.Sscanf(string(data), "%d", &pid); scanErr != nil {
			t.Fatal(scanErr)
		}
		deadline := time.Now().Add(time.Second)
		for {
			killErr := syscall.Kill(pid, 0)
			if errors.Is(killErr, syscall.ESRCH) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("child process %d survived cancellation: %v", pid, killErr)
			}
			runtime.Gosched()
		}
	})
}

func TestExecutorHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_BACKUP_HELPER") != "1" {
		return
	}
	if len(os.Args) < 3 {
		return
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	switch os.Args[separator+1] {
	case "output":
		_, _ = os.Stdout.Write(make([]byte, CommandOutputLimit+1))
	case "stdin":
		data, err := os.ReadFile("/dev/stdin")
		if err != nil || len(data) != 0 {
			os.Exit(12)
		}
	case "cancel":
		if separator+2 >= len(os.Args) {
			return
		}
		pidFile := os.Args[separator+2]
		command := os.Args[0]
		child, err := os.StartProcess(command, []string{command, "-test.run=TestExecutorHelperChild"}, &os.ProcAttr{
			Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
			Env:   []string{"GO_WANT_BACKUP_HELPER=1"},
		})
		if err != nil {
			os.Exit(13)
		}
		if err := os.WriteFile(pidFile, []byte(fmt.Sprint(child.Pid)), 0o600); err != nil {
			os.Exit(14)
		}
		select {}
	}
}

func TestExecutorHelperChild(t *testing.T) {
	if os.Getenv("GO_WANT_BACKUP_HELPER") != "1" {
		return
	}
	select {}
}

func TestFileSecretsRequireOwnerOnlyRegularNonSymlinkFiles(t *testing.T) {
	root := t.TempDir()
	write := func(name string, mode os.FileMode) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("value\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	valid := write(string(SecretDatabasePassword), 0o400)
	secrets := fileSecretsAt(root)
	if got, err := secrets.Read(SecretDatabasePassword); err != nil || got != "value" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if err := os.Remove(valid); err != nil {
		t.Fatal(err)
	}
	world := write(string(SecretDatabasePassword), 0o444)
	if _, err := secrets.Read(SecretDatabasePassword); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("world-readable accepted: %v", err)
	}
	if err := os.Remove(world); err != nil {
		t.Fatal(err)
	}
	target := write("target", 0o400)
	if err := os.Symlink(target, filepath.Join(root, string(SecretDatabasePassword))); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Read(SecretDatabasePassword); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("symlink accepted: %v", err)
	}
	if err := os.Remove(filepath.Join(root, string(SecretDatabasePassword))); err != nil {
		t.Fatal(err)
	}
	write(string(SecretDatabasePassword), 0o400)
	symlinkRoot := filepath.Join(t.TempDir(), "linked-secrets")
	if err := os.Symlink(root, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := fileSecretsAt(symlinkRoot).Read(SecretDatabasePassword); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("symlink secret root accepted: %v", err)
	}
}
