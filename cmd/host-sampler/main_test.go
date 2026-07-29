package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPayloadCanonicalizesAllowlistedComposeStatsAndFilesystems(t *testing.T) {
	input := `{
	  "schemaVersion": 1,
	  "observedAt": "2026-07-30T04:05:06Z",
	  "compose": [
	    {"service":"backup","state":"exited","health":""},
	    {"service":"redis","state":"exited","health":""},
	    {"service":"app","state":"running","health":"healthy","restarts":1}
	  ],
	  "stats": [
	    {"service":"app","cpuPercent":"12.50%","memoryUsage":"1.5MiB / 2GiB"}
	  ],
	  "filesystems": [
	    {"filesystem":"backup","usedPercent":"75%"},
	    {"filesystem":"root","usedPercent":"37.5%"}
	  ]
	}`
	var stdout, stderr bytes.Buffer
	if code := run(
		[]string{"payload"},
		strings.NewReader(input),
		&stdout,
		&stderr,
		fixedNow,
	); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	want := `{"schemaVersion":1,"observedAt":"2026-07-30T04:05:06Z","services":[{"service":"caddy","up":false,"cpuPercent":0,"memoryBytes":0,"memoryLimitBytes":0,"restarts":null},{"service":"app","up":true,"cpuPercent":12.5,"memoryBytes":1572864,"memoryLimitBytes":2147483648,"restarts":1},{"service":"worker","up":false,"cpuPercent":0,"memoryBytes":0,"memoryLimitBytes":0,"restarts":null},{"service":"postgres","up":false,"cpuPercent":0,"memoryBytes":0,"memoryLimitBytes":0,"restarts":null},{"service":"redis","up":false,"cpuPercent":0,"memoryBytes":0,"memoryLimitBytes":0,"restarts":null},{"service":"minio","up":false,"cpuPercent":0,"memoryBytes":0,"memoryLimitBytes":0,"restarts":null}],"filesystems":[{"filesystem":"root","usedPercent":37.5},{"filesystem":"backup","usedPercent":75}]}` + "\n"
	if stdout.String() != want {
		t.Fatalf("stdout=%q want=%q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestPayloadRejectsUnknownDockerSensitiveFields(t *testing.T) {
	for _, field := range []string{
		`"command":"print-secret"`,
		`"environment":["PASSWORD=secret"]`,
		`"mounts":["/var/run/docker.sock"]`,
		`"imageRegistryAuth":"secret-token"`,
		`"logs":"secret-log-line"`,
	} {
		t.Run(field[:strings.IndexByte(field, ':')], func(t *testing.T) {
			input := fmt.Sprintf(
				`{"schemaVersion":1,"observedAt":"2026-07-30T04:05:06Z","compose":[{"service":"app","state":"running","health":"healthy","restarts":0,%s}],"stats":[{"service":"app","cpuPercent":"1%%","memoryUsage":"1MiB / 2MiB"}],"filesystems":[]}`,
				field,
			)
			assertInvalidPayload(t, input)
		})
	}
}

func TestPayloadRejectsUnsafeRowsAndBrokenInput(t *testing.T) {
	base := `{"schemaVersion":1,"observedAt":"2026-07-30T04:05:06Z","compose":[%s],"stats":[%s],"filesystems":[%s]}`
	tests := map[string]string{
		"unknown service": fmt.Sprintf(
			base,
			`{"service":"database-prod-1","state":"running","health":"healthy","restarts":0}`,
			`{"service":"database-prod-1","cpuPercent":"1%","memoryUsage":"1MiB / 2MiB"}`,
			``,
		),
		"duplicate service": fmt.Sprintf(
			base,
			`{"service":"app","state":"running","health":"healthy","restarts":0},{"service":"app","state":"running","health":"healthy","restarts":0}`,
			`{"service":"app","cpuPercent":"1%","memoryUsage":"1MiB / 2MiB"}`,
			``,
		),
		"duplicate stats": fmt.Sprintf(
			base,
			`{"service":"app","state":"running","health":"healthy","restarts":0}`,
			`{"service":"app","cpuPercent":"1%","memoryUsage":"1MiB / 2MiB"},{"service":"app","cpuPercent":"1%","memoryUsage":"1MiB / 2MiB"}`,
			``,
		),
		"orphan stats": fmt.Sprintf(
			base,
			`{"service":"app","state":"exited","health":"","restarts":0}`,
			`{"service":"redis","cpuPercent":"1%","memoryUsage":"1MiB / 2MiB"}`,
			``,
		),
		"running without stats": fmt.Sprintf(
			base,
			`{"service":"app","state":"running","health":"healthy","restarts":0}`,
			``,
			``,
		),
		"negative restart": fmt.Sprintf(
			base,
			`{"service":"app","state":"exited","health":"","restarts":-1}`,
			``,
			``,
		),
		"cpu over 100": fmt.Sprintf(
			base,
			`{"service":"app","state":"running","health":"healthy","restarts":0}`,
			`{"service":"app","cpuPercent":"100.1%","memoryUsage":"1MiB / 2MiB"}`,
			``,
		),
		"negative cpu": fmt.Sprintf(
			base,
			`{"service":"app","state":"running","health":"healthy","restarts":0}`,
			`{"service":"app","cpuPercent":"-1%","memoryUsage":"1MiB / 2MiB"}`,
			``,
		),
		"memory exceeds limit": fmt.Sprintf(
			base,
			`{"service":"app","state":"running","health":"healthy","restarts":0}`,
			`{"service":"app","cpuPercent":"1%","memoryUsage":"3MiB / 2MiB"}`,
			``,
		),
		"unknown state": fmt.Sprintf(
			base,
			`{"service":"app","state":"secret-state","health":"","restarts":0}`,
			``,
			``,
		),
		"unknown health": fmt.Sprintf(
			base,
			`{"service":"app","state":"running","health":"secret-health","restarts":0}`,
			`{"service":"app","cpuPercent":"1%","memoryUsage":"1MiB / 2MiB"}`,
			``,
		),
		"unknown filesystem": fmt.Sprintf(
			base,
			``,
			``,
			`{"filesystem":"/private/customer","usedPercent":"1%"}`,
		),
		"duplicate filesystem": fmt.Sprintf(
			base,
			``,
			``,
			`{"filesystem":"root","usedPercent":"1%"},{"filesystem":"root","usedPercent":"2%"}`,
		),
		"filesystem over 100": fmt.Sprintf(
			base,
			``,
			``,
			`{"filesystem":"root","usedPercent":"101%"}`,
		),
		"no rows":     fmt.Sprintf(base, ``, ``, ``),
		"truncated":   `{"schemaVersion":1`,
		"extra value": fmt.Sprintf(base, ``, ``, `{"filesystem":"root","usedPercent":"1%"}`) + `{}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			assertInvalidPayload(t, input)
		})
	}

	rows := make([]string, 0, 17)
	for index := 0; index < 17; index++ {
		rows = append(rows, fmt.Sprintf(
			`{"filesystem":"root","usedPercent":"%d%%"}`,
			index,
		))
	}
	assertInvalidPayload(t, fmt.Sprintf(base, ``, ``, strings.Join(rows, ",")))
	assertInvalidPayload(t, strings.Repeat(" ", 64*1024+1))
}

func TestParseBytesRoundsFractionalDockerUnitsUpWithoutOverflow(t *testing.T) {
	for value, want := range map[string]int64{
		"1.234MiB":            1293943,
		"0.001KiB":            2,
		"1.1kB":               1100,
		"9007199254740990.1B": maxExactValue,
		"9007199254740991B":   maxExactValue,
	} {
		t.Run(value, func(t *testing.T) {
			got, err := parseBytes(value)
			if err != nil || got != want {
				t.Fatalf("parseBytes(%q)=(%d,%v) want=(%d,nil)", value, got, err, want)
			}
		})
	}
	for _, value := range []string{
		"9007199254740991.1B",
		"8796093022208.1KiB",
	} {
		t.Run("overflow "+value, func(t *testing.T) {
			if got, err := parseBytes(value); err == nil || got != 0 {
				t.Fatalf("parseBytes(%q)=(%d,%v) want=(0,error)", value, got, err)
			}
		})
	}
}

func TestPayloadAcceptsOnlyFixedAuxiliaryComposeServices(t *testing.T) {
	for _, service := range []string{
		"postgres-tls-init",
		"minio-data-init",
		"backup-storage-init",
		"backup-secrets-init",
		"backup",
		"migrate",
		"restore",
		"acceptance",
	} {
		t.Run(service, func(t *testing.T) {
			input := fmt.Sprintf(
				`{"schemaVersion":1,"observedAt":"2026-07-30T04:05:06Z","compose":[{"service":%q,"state":"exited","health":"","restarts":null}],"stats":[],"filesystems":[{"filesystem":"root","usedPercent":"1%%"}]}`,
				service,
			)
			var stdout, stderr bytes.Buffer
			code := run(
				[]string{"payload"},
				strings.NewReader(input),
				&stdout,
				&stderr,
				fixedNow,
			)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if strings.Contains(stdout.String(), service) ||
				strings.Count(stdout.String(), `"service":`) != 6 {
				t.Fatalf("unsafe auxiliary projection=%q", stdout.String())
			}
		})
	}
}

func TestSignUsesExactCanonicalBodyAndOwnerOnlySecret(t *testing.T) {
	secret := []byte("test-host-hmac-secret\n")
	path := filepath.Join(t.TempDir(), "hmac")
	if err := os.WriteFile(path, secret, 0o600); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"schemaVersion":1,"observedAt":"2026-07-30T04:05:06Z","services":[{"service":"app","up":true,"cpuPercent":1,"memoryBytes":1,"memoryLimitBytes":2,"restarts":0}],"filesystems":[]}` + "\n")
	timestamp := "1785384306"
	nonce := "0123456789abcdef0123456789abcdef"
	var stdout, stderr bytes.Buffer
	if code := run(
		[]string{"sign", "--secret-file", path, "--timestamp", timestamp, "--nonce", nonce},
		bytes.NewReader(body),
		&stdout,
		&stderr,
		func() time.Time { return time.Unix(1785384306, 0).UTC() },
	); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	mac := hmac.New(sha256.New, []byte("test-host-hmac-secret"))
	_, _ = mac.Write([]byte(timestamp + "\n" + nonce + "\n"))
	_, _ = mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil)) + "\n"
	if stdout.String() != want {
		t.Fatalf("signature=%q want=%q", stdout.String(), want)
	}
}

func TestSignRejectsUnsafeSecretTimestampNonceAndNoncanonicalBody(t *testing.T) {
	validPath := filepath.Join(t.TempDir(), "valid-secret")
	if err := os.WriteFile(validPath, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	unsafePath := filepath.Join(t.TempDir(), "unsafe-secret")
	if err := os.WriteFile(unsafePath, []byte("do-not-leak"), 0o640); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(t.TempDir(), "secret-link")
	if err := os.Symlink(validPath, linkPath); err != nil {
		t.Fatal(err)
	}
	validBody := `{"schemaVersion":1,"observedAt":"2026-07-30T04:05:06Z","services":[],"filesystems":[{"filesystem":"root","usedPercent":1}]}` + "\n"
	unsortedBody := `{"schemaVersion":1,"observedAt":"2026-07-30T04:05:06Z","services":[{"service":"redis","up":false,"cpuPercent":0,"memoryBytes":0,"memoryLimitBytes":0,"restarts":0},{"service":"app","up":true,"cpuPercent":1,"memoryBytes":1,"memoryLimitBytes":2,"restarts":0}],"filesystems":[]}` + "\n"
	tests := []struct {
		name      string
		path      string
		timestamp string
		nonce     string
		body      string
	}{
		{"unsafe mode", unsafePath, "1785384306", strings.Repeat("a", 32), validBody},
		{"symlink", linkPath, "1785384306", strings.Repeat("b", 32), validBody},
		{"past skew", validPath, "1785384215", strings.Repeat("c", 32), validBody},
		{"future skew", validPath, "1785384397", strings.Repeat("d", 32), validBody},
		{"timestamp grammar", validPath, "+1785384306", strings.Repeat("e", 32), validBody},
		{"uppercase nonce", validPath, "1785384306", strings.Repeat("F", 32), validBody},
		{"noncanonical body", validPath, "1785384306", strings.Repeat("0", 32), " " + validBody},
		{"unknown body field", validPath, "1785384306", strings.Repeat("1", 32), strings.Replace(validBody, `}`, `,"secret":"value"}`, 1)},
		{"unsorted body rows", validPath, "1785384306", strings.Repeat("2", 32), unsortedBody},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(
				[]string{
					"sign", "--secret-file", tc.path,
					"--timestamp", tc.timestamp, "--nonce", tc.nonce,
				},
				strings.NewReader(tc.body),
				&stdout,
				&stderr,
				func() time.Time { return time.Unix(1785384306, 0).UTC() },
			)
			if code == 0 || stdout.Len() != 0 {
				t.Fatalf("code=%d stdout=%q", code, stdout.String())
			}
			if stderr.String() != "host sampler: invalid input\n" ||
				strings.Contains(stderr.String(), tc.path) ||
				strings.Contains(stderr.String(), "do-not-leak") {
				t.Fatalf("stderr=%q", stderr.String())
			}
		})
	}
}

func assertInvalidPayload(t *testing.T, input string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"payload"},
		strings.NewReader(input),
		&stdout,
		&stderr,
		fixedNow,
	)
	if code == 0 || stdout.Len() != 0 {
		t.Fatalf("code=%d stdout=%q", code, stdout.String())
	}
	if stderr.String() != "host sampler: invalid input\n" ||
		strings.Contains(stderr.String(), "secret") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func fixedNow() time.Time {
	return time.Date(2026, 7, 30, 4, 5, 6, 0, time.UTC)
}
