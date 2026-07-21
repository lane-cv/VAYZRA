package processing

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestExecRunnerPassesInputAsOneArgumentWithoutShellInterpolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	script := filepath.Join(t.TempDir(), "print-args")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	dangerous := filepath.Join(t.TempDir(), "lesson; touch should-not-exist")
	stdout, _, exit, err := (ExecRunner{}).Run(context.Background(), script, []string{"--", dangerous}, 4096, 4096)
	if err != nil || exit != 0 || string(stdout) != "--\n"+dangerous+"\n" {
		t.Fatalf("stdout=%q exit=%d err=%v", stdout, exit, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dangerous), "should-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("argument was interpreted by a shell")
	}
}

func TestExecRunnerCancelsOnBoundedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	script := filepath.Join(t.TempDir(), "spam")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nwhile :; do printf '0123456789'; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stdout, stderr, _, err := (ExecRunner{}).Run(ctx, script, nil, 64, 64)
	if !errors.Is(err, ErrCommandOutputLimit) || len(stdout) > 64 || len(stderr) > 64 || strings.Contains(err.Error(), script) {
		t.Fatalf("stdout=%d stderr=%d err=%v", len(stdout), len(stderr), err)
	}
}
