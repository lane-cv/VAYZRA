//go:build unix

package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

const (
	executorRecoverySnapshotID = "1111111111111111111111111111111111111111111111111111111111111111"
	executorOtherSnapshotID    = "2222222222222222222222222222222222222222222222222222222222222222"
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
			if len(command.Args) < 2 || command.Args[0] != "--no-cache" {
				t.Fatalf("restic cache is not disabled: %q", command.Args)
			}
			return CommandResult{
				Stdout: []byte(fmt.Sprintf(
					`{"message_type":"summary","files_new":1,"files_changed":0,"files_unmodified":0,"dirs_new":0,"dirs_changed":0,"dirs_unmodified":0,"data_blobs":1,"tree_blobs":1,"data_added":128,"data_added_packed":96,"total_files_processed":1,"total_bytes_processed":22,"total_duration":0.1,"snapshot_id":%q}`,
					executorRecoverySnapshotID,
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
	if result.LocalSnapshotID != executorRecoverySnapshotID ||
		len(result.Manifest.ObjectSnapshotID) != sha256.Size*2 ||
		result.Manifest.ObjectSnapshotID == executorRecoverySnapshotID ||
		result.ManifestSHA256 == [32]byte{} {
		t.Fatalf("result=%+v", result)
	}
	var resticBackups []Command
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
		if filepath.Base(command.Executable) == "restic" {
			resticBackups = append(resticBackups, command)
		}
	}
	if len(resticBackups) != 1 {
		t.Fatalf("restic backups=%d want=1", len(resticBackups))
	}
	manifestHashTag := "happylearn-manifest-sha256:" +
		hex.EncodeToString(result.ManifestSHA256[:])
	for _, required := range []string{
		"happylearn-batch:" + commandRunIDForExecutor,
		manifestHashTag,
		"database.dump",
		"manifest.json",
		"recovery-bundle.age",
	} {
		if !slices.Contains(resticBackups[0].Args, required) {
			t.Errorf("single recovery snapshot missing arg %q: %q", required, resticBackups[0].Args)
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
		if !slices.Equal(
			command.Args,
			[]string{"--no-cache", "check", "--read-data"},
		) {
			t.Fatalf("integrity command=%q", command.Args)
		}
		return CommandResult{
			ExitCode: 1,
			Stderr:   []byte("wrong password repository=/private/local/repository pack=secret"),
		}, nil
	}}
	executor, _ := executorFixture(t, runner)
	_, err := executor.Verify(
		context.Background(),
		VerifyInput{
			RunID:      commandRunIDForExecutor,
			SnapshotID: executorRecoverySnapshotID,
		},
	)
	if !errors.Is(err, ErrIntegrity) || err.Error() != ErrIntegrity.Error() {
		t.Fatalf("err=%v", err)
	}
}

func TestExecutorVerifyBindsExactSnapshotBatchTagAndManifestHash(t *testing.T) {
	manifest := validManifest()
	manifest.BatchID = commandRunIDForExecutor
	manifestBytes, err := MarshalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestHash := sha256.Sum256(manifestBytes)
	runner := verifyRunner(t, executorRecoverySnapshotID, []string{
		"happylearn-batch:" + commandRunIDForExecutor,
		"happylearn-manifest-sha256:" + hex.EncodeToString(manifestHash[:]),
	}, manifestBytes)
	executor, _ := executorFixture(t, runner)

	verified, err := executor.Verify(context.Background(), VerifyInput{
		RunID:          commandRunIDForExecutor,
		SnapshotID:     executorRecoverySnapshotID,
		ManifestSHA256: manifestHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if verified.Manifest != manifest {
		t.Fatalf("manifest=%+v", verified.Manifest)
	}
}

func TestExecutorVerifyRejectsWrongRunSnapshotTagHashAndManifest(t *testing.T) {
	manifest := validManifest()
	manifest.BatchID = commandRunIDForExecutor
	manifestBytes, err := MarshalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestHash := sha256.Sum256(manifestBytes)
	otherRunID := "30000000-0000-4000-8000-000000000003"
	cases := []struct {
		name       string
		snapshotID string
		tags       []string
		manifest   []byte
		expected   [sha256.Size]byte
	}{
		{
			name: "wrong exact snapshot", snapshotID: executorOtherSnapshotID,
			tags: []string{
				"happylearn-batch:" + commandRunIDForExecutor,
				"happylearn-manifest-sha256:" + hex.EncodeToString(manifestHash[:]),
			},
			manifest: manifestBytes, expected: manifestHash,
		},
		{
			name: "wrong run tag", snapshotID: executorRecoverySnapshotID,
			tags: []string{
				"happylearn-batch:" + otherRunID,
				"happylearn-manifest-sha256:" + hex.EncodeToString(manifestHash[:]),
			},
			manifest: manifestBytes, expected: manifestHash,
		},
		{
			name: "wrong manifest hash tag", snapshotID: executorRecoverySnapshotID,
			tags: []string{
				"happylearn-batch:" + commandRunIDForExecutor,
				"happylearn-manifest-sha256:" + strings.Repeat("f", sha256.Size*2),
			},
			manifest: manifestBytes, expected: manifestHash,
		},
		{
			name: "tampered manifest bytes", snapshotID: executorRecoverySnapshotID,
			tags: []string{
				"happylearn-batch:" + commandRunIDForExecutor,
				"happylearn-manifest-sha256:" + hex.EncodeToString(manifestHash[:]),
			},
			manifest: append(append([]byte(nil), manifestBytes...), '\n'),
			expected: manifestHash,
		},
		{
			name: "mismatched expected manifest", snapshotID: executorRecoverySnapshotID,
			tags: []string{
				"happylearn-batch:" + commandRunIDForExecutor,
				"happylearn-manifest-sha256:" + strings.Repeat("e", sha256.Size*2),
			},
			manifest: manifestBytes,
			expected: [sha256.Size]byte{1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := verifyRunner(t, tc.snapshotID, tc.tags, tc.manifest)
			executor, _ := executorFixture(t, runner)
			_, err := executor.Verify(context.Background(), VerifyInput{
				RunID:          commandRunIDForExecutor,
				SnapshotID:     executorRecoverySnapshotID,
				ManifestSHA256: tc.expected,
			})
			if !errors.Is(err, ErrIntegrity) || err.Error() != ErrIntegrity.Error() {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func verifyRunner(
	t *testing.T,
	snapshotID string,
	tags []string,
	manifest []byte,
) *recordingRunner {
	t.Helper()
	return &recordingRunner{run: func(_ context.Context, command Command, _ int) (CommandResult, error) {
		if len(command.Args) < 2 || command.Args[0] != "--no-cache" {
			t.Fatalf("restic cache is not disabled: %q", command.Args)
		}
		args := command.Args[1:]
		switch {
		case slices.Equal(args, []string{"check", "--read-data"}):
			return CommandResult{ExitCode: 0}, nil
		case len(args) == 3 &&
			args[0] == "snapshots" &&
			args[1] == "--json":
			return CommandResult{
				ExitCode: 0,
				Stdout: []byte(fmt.Sprintf(
					`[{"id":%q,"tags":%s}]`,
					snapshotID,
					mustJSON(t, tags),
				)),
			}, nil
		case len(args) == 4 &&
			args[0] == "stats" &&
			args[1] == "--json":
			return CommandResult{
				ExitCode: 0,
				Stdout: []byte(
					`{"total_size":558,"total_uncompressed_size":1837,"compression_ratio":3.292114695340502,"compression_progress":100,"compression_space_saving":69.62438758845944,"total_blob_count":3,"snapshots_count":1}`,
				),
			}, nil
		case len(args) == 3 &&
			args[0] == "dump" &&
			args[1] == executorRecoverySnapshotID &&
			args[2] == "manifest.json":
			return CommandResult{ExitCode: 0, Stdout: append([]byte(nil), manifest...)}, nil
		default:
			t.Fatalf("unexpected verify command: %q", command.Args)
			return CommandResult{}, nil
		}
	}}
}

func TestDecodeResticStatsMatchesRestic0191RawDataSchema(t *testing.T) {
	const representative = `{"total_size":558,"total_uncompressed_size":1837,"compression_ratio":3.292114695340502,"compression_progress":100,"compression_space_saving":69.62438758845944,"total_blob_count":3,"snapshots_count":1}`
	if err := decodeResticStats([]byte(representative)); err != nil {
		t.Fatalf("representative stats: %v", err)
	}
	var valid map[string]any
	if err := json.Unmarshal([]byte(representative), &valid); err != nil {
		t.Fatal(err)
	}
	for key := range valid {
		t.Run("missing-"+key, func(t *testing.T) {
			candidate := cloneJSONMap(valid)
			delete(candidate, key)
			if decodeResticStats([]byte(mustJSON(t, candidate))) == nil {
				t.Fatalf("accepted missing %q", key)
			}
		})
		t.Run("null-"+key, func(t *testing.T) {
			candidate := cloneJSONMap(valid)
			candidate[key] = nil
			if decodeResticStats([]byte(mustJSON(t, candidate))) == nil {
				t.Fatalf("accepted null %q", key)
			}
		})
	}
	for name, encoded := range map[string]string{
		"unknown":                 strings.TrimSuffix(representative, "}") + `,"future_field":1}`,
		"negative-total-size":     strings.Replace(representative, `"total_size":558`, `"total_size":-1`, 1),
		"negative-uncompressed":   strings.Replace(representative, `"total_uncompressed_size":1837`, `"total_uncompressed_size":-1`, 1),
		"negative-blob-count":     strings.Replace(representative, `"total_blob_count":3`, `"total_blob_count":-1`, 1),
		"fractional-total-size":   strings.Replace(representative, `"total_size":558`, `"total_size":1.5`, 1),
		"overflow-total-size":     strings.Replace(representative, `"total_size":558`, `"total_size":9223372036854775808`, 1),
		"negative-ratio":          strings.Replace(representative, `"compression_ratio":3.292114695340502`, `"compression_ratio":-1`, 1),
		"overflow-ratio":          strings.Replace(representative, `"compression_ratio":3.292114695340502`, `"compression_ratio":1e309`, 1),
		"negative-progress":       strings.Replace(representative, `"compression_progress":100`, `"compression_progress":-1`, 1),
		"progress-over-100":       strings.Replace(representative, `"compression_progress":100`, `"compression_progress":101`, 1),
		"space-saving-over-100":   strings.Replace(representative, `"compression_space_saving":69.62438758845944`, `"compression_space_saving":101`, 1),
		"wrong-snapshots-count":   strings.Replace(representative, `"snapshots_count":1`, `"snapshots_count":2`, 1),
		"fractional-blob-count":   strings.Replace(representative, `"total_blob_count":3`, `"total_blob_count":1.5`, 1),
		"overflow-snapshot-count": strings.Replace(representative, `"snapshots_count":1`, `"snapshots_count":9223372036854775808`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if decodeResticStats([]byte(encoded)) == nil {
				t.Fatalf("accepted %s", encoded)
			}
		})
	}
}

func cloneJSONMap(value map[string]any) map[string]any {
	cloned := make(map[string]any, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestExecutorSyncVerifiesDistinctAuthenticatedRemoteSnapshot(t *testing.T) {
	manifest := validManifest()
	manifest.BatchID = commandRunIDForExecutor
	manifestBytes, err := MarshalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestHash := sha256.Sum256(manifestBytes)
	destinationID := strings.Repeat("3", sha256.Size*2)
	accessKey := "remote-access-key-secret"
	secretKey := "remote-secret-key-secret"
	runner := &recordingRunner{run: func(_ context.Context, command Command, _ int) (CommandResult, error) {
		joined := strings.Join(command.Args, "\x00")
		for _, forbidden := range []string{
			"/private/local/repository",
			"s3:https://objects.example.test/backups/happylearn",
			"local-password-secret",
			"remote-password-secret",
			accessKey,
			secretKey,
		} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("sync argv leaked %q", forbidden)
			}
		}
		if len(command.Args) < 2 || command.Args[0] != "--no-cache" {
			t.Fatalf("args=%q", command.Args)
		}
		hasAccessKey := slices.Contains(command.Env, "AWS_ACCESS_KEY_ID="+accessKey)
		hasSecretKey := slices.Contains(command.Env, "AWS_SECRET_ACCESS_KEY="+secretKey)
		for _, environment := range command.Env {
			if strings.Contains(strings.ToLower(environment), "insecure") ||
				strings.HasPrefix(environment, "RESTIC_INSECURE_TLS=") {
				t.Fatalf("TLS bypass in child environment: %q", command.Env)
			}
		}
		switch command.Args[1] {
		case "copy":
			if !hasAccessKey || !hasSecretKey {
				t.Fatalf("copy missing S3 credentials: %q", command.Env)
			}
			return CommandResult{ExitCode: 0}, nil
		case "snapshots":
			if !hasAccessKey || !hasSecretKey {
				t.Fatalf("snapshot lookup missing S3 credentials: %q", command.Env)
			}
			if !slices.Equal(command.Args, []string{
				"--no-cache",
				"snapshots",
				"--json",
				"--tag",
				"happylearn-batch:" + commandRunIDForExecutor,
				"--tag",
				"happylearn-manifest-sha256:" + hex.EncodeToString(manifestHash[:]),
			}) {
				t.Fatalf("snapshot lookup args=%q", command.Args)
			}
			return CommandResult{
				ExitCode: 0,
				Stdout: []byte(fmt.Sprintf(
					`[{"id":%q,"short_id":"33333333","original":%q,"tags":%s}]`,
					destinationID,
					executorRecoverySnapshotID,
					mustJSON(t, []string{
						"happylearn-batch:" + commandRunIDForExecutor,
						"happylearn-manifest-sha256:" + hex.EncodeToString(manifestHash[:]),
					}),
				)),
			}, nil
		case "check":
			return CommandResult{ExitCode: 0}, nil
		case "stats":
			return CommandResult{
				ExitCode: 0,
				Stdout: []byte(
					`{"total_size":558,"total_uncompressed_size":1837,"compression_ratio":3.292114695340502,"compression_progress":100,"compression_space_saving":69.62438758845944,"total_blob_count":3,"snapshots_count":1}`,
				),
			}, nil
		case "dump":
			return CommandResult{
				ExitCode: 0,
				Stdout:   append([]byte(nil), manifestBytes...),
			}, nil
		default:
			t.Fatalf("unexpected remote command: %q", command.Args)
			return CommandResult{}, nil
		}
	}}
	executor, workRoot := executorFixture(t, runner)
	executor.config.Secrets = mapSecrets{
		SecretDatabasePassword: "database-password-secret",
		SecretLocalRepository:  "/private/local/repository",
		SecretLocalPassword:    "local-password-secret",
		SecretRemoteRepository: "s3:https://objects.example.test/backups/happylearn",
		SecretRemotePassword:   "remote-password-secret",
		SecretRemoteAccessKey:  accessKey,
		SecretRemoteSecretKey:  secretKey,
	}
	configured, err := executor.RemoteConfigured()
	if err != nil || !configured {
		t.Fatalf("configured=%v err=%v", configured, err)
	}
	remoteSnapshotID, err := executor.Sync(
		context.Background(),
		SyncInput{
			RunID:            commandRunIDForExecutor,
			SourceSnapshotID: executorRecoverySnapshotID,
			ManifestSHA256:   manifestHash,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if remoteSnapshotID != destinationID {
		t.Fatalf("remoteSnapshotID=%q", remoteSnapshotID)
	}
	if len(runner.calls()) != 5 {
		t.Fatalf("remote commands=%+v", runner.calls())
	}
	entries, err := os.ReadDir(workRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("sync source files remain: %v", entries)
	}
}

func TestDecodeResticCopiedSnapshotRequiresUniqueFullDestinationBinding(t *testing.T) {
	manifestHash := sha256.Sum256([]byte("manifest"))
	destinationID := strings.Repeat("3", sha256.Size*2)
	valid := fmt.Sprintf(
		`[{"time":"2026-07-28T01:02:03Z","tree":%q,"paths":["/work/recovery"],"hostname":"backup","username":"happylearn-backup","uid":10003,"tags":%s,"program_version":"restic 0.19.1","summary":{},"id":%q,"short_id":"33333333","original":%q}]`,
		strings.Repeat("4", sha256.Size*2),
		mustJSON(t, []string{
			"happylearn-batch:" + commandRunIDForExecutor,
			"happylearn-manifest-sha256:" + hex.EncodeToString(manifestHash[:]),
		}),
		destinationID,
		executorRecoverySnapshotID,
	)
	got, err := decodeResticCopiedSnapshot(
		[]byte(valid),
		executorRecoverySnapshotID,
		commandRunIDForExecutor,
		manifestHash,
	)
	if err != nil || got != destinationID {
		t.Fatalf("destination=%q err=%v", got, err)
	}
	for name, encoded := range map[string]string{
		"empty":            `[]`,
		"multiple":         strings.TrimSuffix(valid, "]") + "," + strings.TrimPrefix(valid, "["),
		"source-reused":    strings.Replace(valid, destinationID, executorRecoverySnapshotID, 1),
		"short-id":         strings.Replace(valid, destinationID, "3333333333333333", 1),
		"wrong-original":   strings.Replace(valid, executorRecoverySnapshotID, executorOtherSnapshotID, 1),
		"missing-original": strings.Replace(valid, `,"original":"`+executorRecoverySnapshotID+`"`, "", 1),
		"wrong-run-tag":    strings.Replace(valid, commandRunIDForExecutor, "30000000-0000-4000-8000-000000000003", 1),
		"extra-tag":        strings.Replace(valid, `"tags":[`, `"tags":["extra",`, 1),
		"unknown-field":    strings.Replace(valid, `,"short_id"`, `,"future":1,"short_id"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := decodeResticCopiedSnapshot(
				[]byte(encoded),
				executorRecoverySnapshotID,
				commandRunIDForExecutor,
				manifestHash,
			); err == nil || got != "" {
				t.Fatalf("destination=%q err=%v encoded=%s", got, err, encoded)
			}
		})
	}
}

const commandRunIDForExecutor = "10000000-0000-4000-8000-000000000001"

func TestExecutorRejectsPartialRemoteConfiguration(t *testing.T) {
	executor, _ := executorFixture(t, &recordingRunner{})
	remoteTuple := mapSecrets{
		SecretRemoteRepository: "s3:https://objects.example.test/backups",
		SecretRemotePassword:   "remote-password-secret",
		SecretRemoteAccessKey:  "remote-access-key-secret",
		SecretRemoteSecretKey:  "remote-secret-key-secret",
	}
	for missing := range remoteTuple {
		t.Run("missing-"+string(missing), func(t *testing.T) {
			secrets := mapSecrets{}
			for name, value := range remoteTuple {
				if name != missing {
					secrets[name] = value
				}
			}
			executor.config.Secrets = secrets
			if configured, err := executor.RemoteConfigured(); configured ||
				!errors.Is(err, ErrRemoteSync) {
				t.Fatalf("configured=%v err=%v", configured, err)
			}
		})
	}
	executor.config.Secrets = mapSecrets{}
	if configured, err := executor.RemoteConfigured(); configured || err != nil {
		t.Fatalf("empty configured=%v err=%v", configured, err)
	}
	for _, repository := range []string{
		"s3:http://objects.example.test/backups",
		"s3:https://user:password@objects.example.test/backups",
		"s3:https://objects.example.test",
		"s3:https://objects.example.test/backups?insecure=true",
		"s3:https://objects.example.test/backups#insecure-tls",
		"https://objects.example.test/backups",
	} {
		t.Run(repository, func(t *testing.T) {
			secrets := mapSecrets{}
			for name, value := range remoteTuple {
				secrets[name] = value
			}
			secrets[SecretRemoteRepository] = repository
			executor.config.Secrets = secrets
			if configured, err := executor.RemoteConfigured(); configured ||
				!errors.Is(err, ErrRemoteSync) {
				t.Fatalf("configured=%v err=%v", configured, err)
			}
		})
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
	for _, name := range []SecretName{
		SecretRemoteAccessKey,
		SecretRemoteSecretKey,
	} {
		write(string(name), 0o400)
		if got, err := secrets.Read(name); err != nil || got != "value" {
			t.Fatalf("name=%q got=%q err=%v", name, got, err)
		}
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
