package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadPasswordFileRejectsInsecureAndEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "password")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPasswordFile(path); err == nil {
		t.Fatal("empty password accepted")
	}
	if err := os.WriteFile(path, []byte("Temporary Password 42!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPasswordFile(path)
	if err != nil || string(got) != "Temporary Password 42!" {
		t.Fatalf("password=%q err=%v", got, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readPasswordFile(path); err == nil {
			t.Fatal("world-readable password accepted")
		}
	}
}

func TestParseCreateTeacherRequiresNamedPasswordFile(t *testing.T) {
	_, err := parseCreateTeacher([]string{"--username", "admin", "--display-name", "老师", "secret"})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error=%v", err)
	}
}
