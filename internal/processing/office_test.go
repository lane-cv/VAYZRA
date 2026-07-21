package processing

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfficeConverterUsesFreshProfileAndValidatesPDF(t *testing.T) {
	work := t.TempDir()
	input := filepath.Join(work, "opaque-input")
	if err := os.WriteFile(input, []byte("office"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &runnerStub{hook: func(args []string) {
		out := argAfter(args, "--outdir")
		if out == "" {
			t.Fatal("missing outdir")
		}
		if err := os.WriteFile(filepath.Join(out, "opaque-input.pdf"), []byte("%PDF-1.7\n%%EOF\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}}
	output, err := (OfficeConverter{Runner: runner, Executable: "soffice"}).Convert(context.Background(), input, work)
	if err != nil || !strings.HasSuffix(output, ".pdf") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	joined := strings.Join(runner.args, " ")
	if !strings.Contains(joined, "-env:UserInstallation=file://") || strings.Contains(joined, "sh -c") {
		t.Fatalf("args=%q", runner.args)
	}
}

func TestOfficeConverterRejectsMissingOrInvalidOutput(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		work := t.TempDir()
		input := filepath.Join(work, "input")
		_ = os.WriteFile(input, []byte("x"), 0600)
		runner := &runnerStub{hook: func(args []string) {
			if invalid {
				_ = os.WriteFile(filepath.Join(argAfter(args, "--outdir"), "input.pdf"), []byte("not pdf"), 0600)
			}
		}}
		_, err := (OfficeConverter{Runner: runner, Executable: "soffice"}).Convert(context.Background(), input, work)
		if category(err) != "conversion_failed" {
			t.Fatalf("invalid=%t err=%v", invalid, err)
		}
	}
}

func argAfter(args []string, value string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == value {
			return args[i+1]
		}
	}
	return ""
}
