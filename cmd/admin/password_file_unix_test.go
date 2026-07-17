//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPasswordFileRejectsSymlinkInsecureAndOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("Temporary Password 42!\n"), 0o600); err != nil { t.Fatal(err) }
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil { t.Fatal(err) }
	if _, err := readPasswordFile(link); err == nil { t.Fatal("symlink accepted") }
	if err := os.Chmod(target, 0o640); err != nil { t.Fatal(err) }
	if _, err := readPasswordFile(target); err == nil { t.Fatal("insecure mode accepted") }
	if err := os.Chmod(target, 0o600); err != nil { t.Fatal(err) }
	if err := os.WriteFile(target, make([]byte, maxPasswordFileBytes+1), 0o600); err != nil { t.Fatal(err) }
	if _, err := readPasswordFile(target); err == nil { t.Fatal("oversized password accepted") }
}
