package processing

import (
	"bytes"
	"context"
	"io"
	"os"
	"unicode/utf8"
)

const MaxAITextBytes = 2 << 20

func ExtractPDFText(ctx context.Context, runner Runner, inputPDF, outputText string) (string, error) {
	if runner == nil || inputPDF == "" || outputText == "" {
		return "", reject("text_extraction_failed")
	}
	_, _, exit, err := runner.Run(ctx, "pdftotext", []string{"-layout", "--", inputPDF, outputText}, 64<<10, 64<<10)
	if err != nil || exit != 0 {
		return "", reject("text_extraction_failed")
	}
	file, err := os.Open(outputText)
	if err != nil {
		return "", reject("text_extraction_failed")
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, MaxAITextBytes+1))
	if err != nil {
		return "", transient("workspace_unavailable")
	}
	text, err := NormalizeAIText(body)
	if err != nil || text == "" {
		return "", reject("text_extraction_failed")
	}
	return text, nil
}

func readAITextFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", transient("workspace_unavailable")
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, MaxAITextBytes+1))
	if err != nil {
		return "", transient("workspace_unavailable")
	}
	return NormalizeAIText(body)
}

func NormalizeAIText(body []byte) (string, error) {
	if len(body) > MaxAITextBytes || !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
		return "", reject("text_extraction_failed")
	}
	body = bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
	body = bytes.ReplaceAll(body, []byte("\r"), []byte("\n"))
	return string(body), nil
}
