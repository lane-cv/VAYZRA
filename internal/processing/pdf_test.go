package processing

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestPDFValidatorUsesBoundedParserAndStableErrors(t *testing.T) {
	path := "/work/job/input;touch pwned.pdf"
	for _, tc := range []struct {
		exit   int
		runErr error
		stdout []byte
		want   string
	}{
		{exit: 0, stdout: []byte("Pages: 1\n")},
		{exit: 1, want: "malformed_pdf"},
		{exit: -1, runErr: errors.New("start"), want: "parser_unavailable"},
	} {
		runner := &runnerStub{exit: tc.exit, err: tc.runErr, stdout: tc.stdout}
		err := (PDFValidator{Runner: runner, Executable: "pdfinfo"}).Validate(context.Background(), path)
		if category(err) != tc.want {
			t.Fatalf("exit=%d err=%v", tc.exit, err)
		}
		wantArgs := []string{"-box", "--", path}
		if runner.executable != "pdfinfo" || !reflect.DeepEqual(runner.args, wantArgs) {
			t.Fatalf("executable=%q args=%q", runner.executable, runner.args)
		}
	}
}
