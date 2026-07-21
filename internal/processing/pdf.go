package processing

import "context"

type PDFValidator struct {
	Runner     Runner
	Executable string
}

func (v PDFValidator) Validate(ctx context.Context, path string) error {
	if v.Runner == nil || path == "" {
		return transient("parser_unavailable")
	}
	executable := v.Executable
	if executable == "" {
		executable = "pdfinfo"
	}
	_, _, exit, err := v.Runner.Run(ctx, executable, []string{"-box", "--", path}, 256*1024, 64*1024)
	if err != nil {
		return transient("parser_unavailable")
	}
	if exit != 0 {
		return reject("malformed_pdf")
	}
	return nil
}
