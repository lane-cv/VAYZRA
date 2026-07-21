package main

import (
	"context"
	"testing"
)

func TestMaintenanceCleanupFilesParsesBoundedLimit(t *testing.T) {
	called := 0
	cleanup := func(_ context.Context, limit int) error {
		called = limit
		return nil
	}
	if err := runCommand(context.Background(), []string{"cleanup-files", "--limit", "25"}, cleanup); err != nil || called != 25 {
		t.Fatalf("called=%d err=%v", called, err)
	}
	called = 0
	if err := runCommand(context.Background(), []string{"cleanup-files"}, cleanup); err != nil || called != 100 {
		t.Fatalf("default called=%d err=%v", called, err)
	}
	for _, args := range [][]string{{}, {"unknown"}, {"cleanup-files", "--limit", "0"}, {"cleanup-files", "--limit", "1001"}, {"cleanup-files", "--unexpected"}} {
		if err := runCommand(context.Background(), args, cleanup); err == nil {
			t.Fatalf("args=%q accepted", args)
		}
	}
}
