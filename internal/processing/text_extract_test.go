package processing

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAITextPDFUsesSafeLayoutInvocation(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.pdf")
	output := filepath.Join(dir, "output.txt")
	runner := &extractRunner{hook: func(executable string, args []string) {
		if executable == "pdftotext" {
			if err := os.WriteFile(output, []byte("line one\r\nline two\r"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}}
	text, err := ExtractPDFText(context.Background(), runner, input, output)
	if err != nil {
		t.Fatal(err)
	}
	if runner.executable != "pdftotext" || !reflect.DeepEqual(runner.args, []string{"-layout", "--", input, output}) {
		t.Fatalf("call=%q %#v", runner.executable, runner.args)
	}
	if text != "line one\nline two\n" {
		t.Fatalf("text=%q", text)
	}
}

func TestAITextNormalizationRejectsUnsafeContentAndCapsTwoMiB(t *testing.T) {
	if got, err := NormalizeAIText([]byte("a\r\nb\rc")); err != nil || got != "a\nb\nc" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	for _, body := range [][]byte{{0xff}, []byte("before\x00after"), []byte(strings.Repeat("x", MaxAITextBytes+1))} {
		if _, err := NormalizeAIText(body); err == nil {
			t.Fatalf("accepted unsafe body of %d bytes", len(body))
		}
	}
	if got, err := NormalizeAIText([]byte(strings.Repeat("x", MaxAITextBytes))); err != nil || len(got) != MaxAITextBytes {
		t.Fatalf("boundary len=%d err=%v", len(got), err)
	}
}

func TestAITextOfficePDFIsPassedToSameExtractor(t *testing.T) {
	dir := t.TempDir()
	converted := filepath.Join(dir, "converted.pdf")
	output := filepath.Join(dir, "converted.txt")
	runner := &extractRunner{hook: func(executable string, _ []string) {
		if executable == "pdftotext" {
			_ = os.WriteFile(output, []byte("office text"), 0600)
		}
	}}
	got, err := ExtractPDFText(context.Background(), runner, converted, output)
	if err != nil || got != "office text" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if !reflect.DeepEqual(runner.args, []string{"-layout", "--", converted, output}) {
		t.Fatalf("args=%#v", runner.args)
	}
}

type extractRunner struct {
	executable string
	args       []string
	hook       func(string, []string)
	err        error
	exit       int
}

func (r *extractRunner) Run(_ context.Context, executable string, args []string, _, _ int64) ([]byte, []byte, int, error) {
	r.executable = executable
	r.args = append([]string(nil), args...)
	if r.hook != nil {
		r.hook(executable, args)
	}
	if r.err != nil {
		return nil, nil, -1, r.err
	}
	if r.exit != 0 {
		return nil, nil, r.exit, nil
	}
	return nil, nil, 0, nil
}

func TestAITextExtractorRejectsCommandFailure(t *testing.T) {
	_, err := ExtractPDFText(context.Background(), &extractRunner{err: errors.New("runner")}, "input.pdf", "output.txt")
	if category(err) != "text_extraction_failed" {
		t.Fatalf("err=%v", err)
	}
}
