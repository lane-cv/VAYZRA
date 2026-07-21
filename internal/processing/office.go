package processing

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
)

type OfficeConverter struct {
	Runner     Runner
	Executable string
}

func (c OfficeConverter) Convert(ctx context.Context, inputPath, workRoot string) (string, error) {
	if c.Runner == nil || inputPath == "" || workRoot == "" {
		return "", transient("conversion_failed")
	}
	profile, err := os.MkdirTemp(workRoot, "lo-profile-")
	if err != nil {
		return "", transient("conversion_failed")
	}
	defer os.RemoveAll(profile)
	out, err := os.MkdirTemp(workRoot, "lo-output-")
	if err != nil {
		return "", transient("conversion_failed")
	}
	executable := c.Executable
	if executable == "" {
		executable = "soffice"
	}
	profileURL := (&url.URL{Scheme: "file", Path: profile}).String()
	_, _, exit, runErr := c.Runner.Run(ctx, executable, []string{"--headless", "--nologo", "--nodefault", "--nofirststartwizard", "-env:UserInstallation=" + profileURL, "--convert-to", "pdf", "--outdir", out, inputPath}, 64*1024, 64*1024)
	if runErr != nil || exit != 0 {
		return "", transient("conversion_failed")
	}
	base := filepath.Base(inputPath)
	output := filepath.Join(out, base[:len(base)-len(filepath.Ext(base))]+".pdf")
	f, err := os.Open(output)
	if err != nil {
		return "", transient("conversion_failed")
	}
	defer f.Close()
	header := make([]byte, 5)
	if _, err = f.Read(header); err != nil || string(header) != "%PDF-" || validatePDF(f) != nil {
		return "", transient("conversion_failed")
	}
	return output, nil
}
