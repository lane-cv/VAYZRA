package secretfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReadAcceptsOnlyOwnerSafeRegularFilesAndTrimsOneLineEnding(t *testing.T) {
	for name, fixture := range map[string]struct {
		body string
		want string
	}{
		"without newline": {body: "secret-value", want: "secret-value"},
		"line feed":       {body: "secret-value\n", want: "secret-value"},
		"windows newline": {body: "secret-value\r\n", want: "secret-value"},
		"one newline only": {
			body: "secret-value\n\n",
			want: "secret-value\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := writeSecretFixture(t, fixture.body, 0o600)
			got, err := Read(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != fixture.want {
				t.Fatalf("Read()=%q want=%q", got, fixture.want)
			}
		})
	}
}

func TestReadRejectsUnsafeOrInvalidFilesWithCategoryOnlyErrors(t *testing.T) {
	secret := "do-not-leak-secret-material"
	tests := []struct {
		name  string
		build func(*testing.T) string
	}{
		{
			name: "missing",
			build: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "sensitive-missing-path")
			},
		},
		{
			name: "symlink",
			build: func(t *testing.T) string {
				target := writeSecretFixture(t, secret, 0o600)
				link := filepath.Join(t.TempDir(), "sensitive-symlink-path")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
		},
		{
			name: "group writable",
			build: func(t *testing.T) string {
				return writeSecretFixture(t, secret, 0o620)
			},
		},
		{
			name: "world writable",
			build: func(t *testing.T) string {
				return writeSecretFixture(t, secret, 0o602)
			},
		},
		{
			name: "directory",
			build: func(t *testing.T) string {
				return t.TempDir()
			},
		},
		{
			name: "empty",
			build: func(t *testing.T) string {
				return writeSecretFixture(t, "", 0o600)
			},
		},
		{
			name: "newline only",
			build: func(t *testing.T) string {
				return writeSecretFixture(t, "\n", 0o600)
			},
		},
		{
			name: "oversized",
			build: func(t *testing.T) string {
				return writeSecretFixture(t, string(bytes.Repeat([]byte("x"), MaxBytes+1)), 0o600)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.build(t)
			got, err := Read(path)
			if !errors.Is(err, ErrInvalid) || got != nil {
				t.Fatalf("Read()=(%q,%v) want=(nil,ErrInvalid)", got, err)
			}
			if strings.Contains(err.Error(), path) ||
				strings.Contains(err.Error(), secret) {
				t.Fatalf("sensitive detail leaked: %q", err)
			}
		})
	}
}

func TestReadRejectsFIFOWorkingWithoutBlockingForAWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret-fifo")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := Read(path)
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("Read(FIFO) error=%v want ErrInvalid", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Read(FIFO) blocked waiting for a writer")
	}
}

func TestResolveReturnsDirectValue(t *testing.T) {
	lookup := mapLookup(map[string]string{"TOKEN": "direct-value"})
	got, err := Resolve(lookup, "TOKEN", 64)
	if err != nil || got != "direct-value" {
		t.Fatalf("Resolve()=(%q,%v), want direct value", got, err)
	}
}

func TestResolveReadsTrimmedRegularFile(t *testing.T) {
	path := writeSecretFixture(t, "file-value\r\n", 0o600)
	got, err := Resolve(mapLookup(map[string]string{"TOKEN_FILE": path}), "TOKEN", 64)
	if err != nil || got != "file-value" {
		t.Fatalf("Resolve()=(%q,%v), want trimmed file value", got, err)
	}
}

func TestResolveRejectsDirectAndFileTogether(t *testing.T) {
	path := writeSecretFixture(t, "file-value", 0o600)
	_, err := Resolve(mapLookup(map[string]string{
		"TOKEN":      "direct-value",
		"TOKEN_FILE": path,
	}), "TOKEN", 64)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Resolve() error=%v, want ErrConflict", err)
	}
}

func TestResolveRejectsSymlink(t *testing.T) {
	target := writeSecretFixture(t, "file-value", 0o600)
	link := filepath.Join(t.TempDir(), "secret-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(mapLookup(map[string]string{"TOKEN_FILE": link}), "TOKEN", 64)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Resolve() error=%v, want ErrInvalid", err)
	}
}

func TestResolveRejectsEmptyFile(t *testing.T) {
	path := writeSecretFixture(t, "", 0o600)
	_, err := Resolve(mapLookup(map[string]string{"TOKEN_FILE": path}), "TOKEN", 64)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Resolve() error=%v, want ErrInvalid", err)
	}
}

func TestResolveRejectsOversizedFile(t *testing.T) {
	path := writeSecretFixture(t, "12345", 0o600)
	_, err := Resolve(mapLookup(map[string]string{"TOKEN_FILE": path}), "TOKEN", 4)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Resolve() error=%v, want ErrInvalid", err)
	}
}

func TestResolveRejectsGroupOrWorldWritableFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o620, 0o602} {
		path := writeSecretFixture(t, "file-value", mode)
		_, err := Resolve(mapLookup(map[string]string{"TOKEN_FILE": path}), "TOKEN", 64)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("Resolve(mode=%o) error=%v, want ErrInvalid", mode, err)
		}
	}
}

func TestResolveDoesNotIncludeValueOrPathInError(t *testing.T) {
	const value = "sensitive-direct-value"
	path := filepath.Join(t.TempDir(), "sensitive-file-path")
	_, err := Resolve(mapLookup(map[string]string{
		"TOKEN":      value,
		"TOKEN_FILE": path,
	}), "TOKEN", 64)
	if err == nil || strings.Contains(err.Error(), value) || strings.Contains(err.Error(), path) || strings.Contains(err.Error(), "TOKEN") {
		t.Fatalf("Resolve() leaked sensitive context: %v", err)
	}
}

func mapLookup(values map[string]string) Lookup {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func writeSecretFixture(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}
