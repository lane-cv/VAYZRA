package safelog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

var fixedTime = time.Date(2026, time.July, 30, 9, 8, 7, 654321000, time.FixedZone("CST", 8*60*60))

func TestJSONLoggerEmitsAllowlistedFields(t *testing.T) {
	var output bytes.Buffer
	logger := newTestLogger(t, &output)

	logger.Info("request.completed",
		Field{Name: "trace_id", Value: "trace-123"},
		Field{Name: "request_id", Value: "request-456"},
		Field{Name: "stage", Value: "serve"},
		Field{Name: "category", Value: "success"},
		Field{Name: "method", Value: "GET"},
		Field{Name: "path", Value: "/admin/files/%E4%B8%AD%E6%96%87"},
		Field{Name: "status", Value: 200},
		Field{Name: "duration_ms", Value: 1500 * time.Millisecond},
		Field{Name: "service", Value: "server"},
		Field{Name: "state", Value: true},
		Field{Name: "count", Value: int32(3)},
		Field{Name: "bytes", Value: uint16(512)},
	)

	record := decodeSingleRecord(t, output.Bytes())
	want := map[string]any{
		"timestamp":   fixedTime.Format(time.RFC3339Nano),
		"level":       "info",
		"event":       "request.completed",
		"trace_id":    "trace-123",
		"request_id":  "request-456",
		"stage":       "serve",
		"category":    "success",
		"method":      "GET",
		"path":        "/admin/files/%E4%B8%AD%E6%96%87",
		"status":      float64(200),
		"duration_ms": float64(1500),
		"service":     "server",
		"state":       true,
		"count":       float64(3),
		"bytes":       float64(512),
	}
	if len(record) != len(want) {
		t.Fatalf("record keys = %v, want %v", record, want)
	}
	for key, expected := range want {
		if got := record[key]; got != expected {
			t.Errorf("%s = %#v, want %#v", key, got, expected)
		}
	}

	output.Reset()
	logger.Error("request.failed", Field{Name: "category", Value: "upstream"})
	if got := decodeSingleRecord(t, output.Bytes())["level"]; got != "error" {
		t.Fatalf("level = %#v, want error", got)
	}
}

func TestJSONLoggerRejectsSecretLikeFieldNames(t *testing.T) {
	names := []string{
		"secret", "session_token", "password_hash", "cookie",
		"authorization", "credential_id", "api_key", "webhook_url",
		"raw_query", "request_body", "file_content",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			logger := newTestLogger(t, &output)
			logger.Info("security.rejected", Field{Name: name, Value: "never-write-this"})
			if output.Len() != 0 {
				t.Fatalf("unsafe field %q produced %q", name, output.String())
			}
		})
	}

	t.Run("unknown", func(t *testing.T) {
		var output bytes.Buffer
		logger := newTestLogger(t, &output)
		logger.Info("security.rejected", Field{Name: "message", Value: "not allowlisted"})
		if output.Len() != 0 {
			t.Fatalf("unknown field produced %q", output.String())
		}
	})
}

func TestJSONLoggerRedactsSecretLikeValues(t *testing.T) {
	const (
		shortSecret = "abcdefgh"
		longSecret  = "abcdefghij"
		otherSecret = "ZXCVBNM-87654321"
	)
	var output bytes.Buffer
	logger, err := New(&output, func() time.Time { return fixedTime }, shortSecret, longSecret, otherSecret)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logger.Info("secret.redaction",
		Field{Name: "stage", Value: "prefix-" + longSecret + "-middle-" + shortSecret + "-" + longSecret},
		Field{Name: "category", Value: otherSecret + otherSecret},
	)

	line := output.String()
	for _, secret := range []string{shortSecret, longSecret, otherSecret} {
		if strings.Contains(line, secret) {
			t.Fatalf("output contains secret marker %q: %q", secret, line)
		}
	}
	record := decodeSingleRecord(t, output.Bytes())
	if got := record["stage"].(string); got != "prefix-hidden-middle-hidden-hidden" {
		t.Fatalf("stage = %q", got)
	}
	if got := record["category"].(string); got != "hiddenhidden" {
		t.Fatalf("category = %q", got)
	}
}

func TestJSONLoggerRedactorCopiesMarkers(t *testing.T) {
	markers := []string{"copy-me-secret"}
	var output bytes.Buffer
	logger, err := New(&output, func() time.Time { return fixedTime }, markers...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	markers[0] = "mutated-secret"

	logger.Info("secret.copy", Field{Name: "stage", Value: "copy-me-secret"})
	if strings.Contains(output.String(), "copy-me-secret") {
		t.Fatalf("output contains original marker: %q", output.String())
	}
}

func TestJSONLoggerNeverWritesConfiguredMarkerBytes(t *testing.T) {
	tests := []struct {
		name    string
		markers []string
		emit    func(Logger)
	}{
		{
			name:    "replacement is itself a marker",
			markers: []string{"redacted"},
			emit: func(logger Logger) {
				logger.Info("secret.final", Field{Name: "stage", Value: "redacted"})
			},
		},
		{
			name:    "structural key is a marker",
			markers: []string{"timestamp"},
			emit: func(logger Logger) {
				logger.Info("secret.final")
			},
		},
		{
			name:    "integer rendering is a marker",
			markers: []string{"12345678"},
			emit: func(logger Logger) {
				logger.Info("secret.final", Field{Name: "count", Value: 12345678})
			},
		},
		{
			name:    "line terminator completes a marker",
			markers: []string{"+08:00\"}\n"},
			emit: func(logger Logger) {
				logger.Info("secret.final")
			},
		},
		{
			name:    "replacement concatenation reconstructs another marker",
			markers: []string{"abcdefgh", "redactedredacted", "hiddenhidden"},
			emit: func(logger Logger) {
				logger.Info("secret.final", Field{Name: "stage", Value: "abcdefghabcdefgh"})
			},
		},
		{
			name:    "multiple distinct markers",
			markers: []string{"abcdefgh", "ijklmnop"},
			emit: func(logger Logger) {
				logger.Info("secret.final", Field{Name: "stage", Value: "abcdefghijklmnopabcdefgh"})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger, err := New(&output, func() time.Time { return fixedTime }, test.markers...)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			test.emit(logger)

			for _, marker := range test.markers {
				if bytes.Contains(output.Bytes(), []byte(marker)) {
					t.Fatalf("final JSON contains configured marker %q: %q", marker, output.Bytes())
				}
			}
		})
	}
}

func TestJSONLoggerRejectsShortSecretMarkers(t *testing.T) {
	for _, marker := range []string{"", "1234567", "密钥"} {
		t.Run(marker, func(t *testing.T) {
			if _, err := New(io.Discard, func() time.Time { return fixedTime }, marker); err == nil {
				t.Fatalf("New accepted %d-byte secret marker", len(marker))
			}
		})
	}
}

func TestJSONLoggerOmitsURLQueryCookiesAndAuthorization(t *testing.T) {
	tests := []struct {
		name  string
		field Field
	}{
		{name: "raw query in path", field: Field{Name: "path", Value: "/download?signature=never"}},
		{name: "fragment in path", field: Field{Name: "path", Value: "/download#private"}},
		{name: "unescaped path", field: Field{Name: "path", Value: "/student files/private"}},
		{name: "absolute URL path", field: Field{Name: "path", Value: "https://example.test/private"}},
		{name: "typed URL", field: Field{Name: "stage", Value: url.URL{Scheme: "https", Host: "example.test", RawQuery: "token=never"}}},
		{name: "headers", field: Field{Name: "stage", Value: http.Header{"Authorization": {"Bearer never"}}}},
		{name: "raw error", field: Field{Name: "category", Value: errors.New("Authorization: Bearer never")}},
		{name: "URL string", field: Field{Name: "stage", Value: "https://example.test/private?token=never"}},
		{name: "network path URL string", field: Field{Name: "category", Value: "//example.test/private"}},
		{name: "authorization string", field: Field{Name: "service", Value: "Authorization: Bearer never"}},
		{name: "cookie string", field: Field{Name: "state", Value: "Cookie: session=never"}},
		{name: "header string", field: Field{Name: "stage", Value: "X-Internal-Secret: never"}},
		{name: "query-shaped string", field: Field{Name: "category", Value: "result?signature=never"}},
		{name: "JSON body string", field: Field{Name: "stage", Value: `{"password":"never"}`}},
		{name: "form body string", field: Field{Name: "category", Value: "session=never&csrf=never"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := newTestLogger(t, &output)
			logger.Info("request.received", test.field)
			if output.Len() != 0 {
				t.Fatalf("unsafe value produced %q", output.String())
			}
		})
	}

	t.Run("escaped query delimiter belongs to path", func(t *testing.T) {
		var output bytes.Buffer
		logger := newTestLogger(t, &output)
		logger.Info("request.received", Field{Name: "path", Value: "/files/a%3Fb"})
		record := decodeSingleRecord(t, output.Bytes())
		if got := record["path"]; got != "/files/a%3Fb" {
			t.Fatalf("path = %#v", got)
		}
	})
}

func TestJSONLoggerRejectsNetworkPathsAndAllowsEscapedAbsolutePaths(t *testing.T) {
	rejected := []string{
		"//example.test/private",
		"//user@example.test/private",
		"/%2Fexample.test/private",
		"/%2Fuser@example.test/private",
	}
	for _, path := range rejected {
		t.Run(path, func(t *testing.T) {
			var output bytes.Buffer
			logger := newTestLogger(t, &output)
			logger.Info("path.rejected", Field{Name: "path", Value: path})
			if output.Len() != 0 {
				t.Fatalf("network path produced %q", output.String())
			}
		})
	}

	accepted := []string{
		"/",
		"/admin/alerts",
		"/files/a%2Fb/%40student/%E4%B8%AD%E6%96%87",
		"/files/%e4%b8%ad",
	}
	for _, path := range accepted {
		t.Run(path, func(t *testing.T) {
			var output bytes.Buffer
			logger := newTestLogger(t, &output)
			logger.Info("path.accepted", Field{Name: "path", Value: path})
			record := decodeSingleRecord(t, output.Bytes())
			if got := record["path"]; got != path {
				t.Fatalf("path = %#v, want %q", got, path)
			}
		})
	}
}

func TestJSONLoggerRevalidatesPathAfterRedaction(t *testing.T) {
	t.Run("short invalid escape is dropped", func(t *testing.T) {
		var output bytes.Buffer
		logger, err := New(&output, func() time.Time { return fixedTime }, "41secret")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		logger.Info("path.redacted", Field{Name: "path", Value: "/%41secret"})
		if output.Len() != 0 {
			t.Fatalf("invalid redacted path produced %q", output.String())
		}
	})

	t.Run("long invalid escape is not hidden by truncation", func(t *testing.T) {
		var output bytes.Buffer
		logger, err := New(&output, func() time.Time { return fixedTime }, "41secret")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		logger.Info("path.redacted", Field{
			Name:  "path",
			Value: "/" + strings.Repeat("a", 238) + "%41secret",
		})
		if output.Len() != 0 {
			t.Fatalf("invalid redacted path produced %q", output.String())
		}
	})

	t.Run("valid redacted path remains usable", func(t *testing.T) {
		var output bytes.Buffer
		logger, err := New(&output, func() time.Time { return fixedTime }, "secret12")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		logger.Info("path.redacted", Field{Name: "path", Value: "/files/secret12"})
		record := decodeSingleRecord(t, output.Bytes())
		if got := record["path"]; got != "/files/hidden" {
			t.Fatalf("path = %#v", got)
		}
	})
}

func TestJSONLoggerBoundsStringsAndNestedValues(t *testing.T) {
	t.Run("caps rendered strings at valid UTF-8 boundary", func(t *testing.T) {
		var output bytes.Buffer
		logger := newTestLogger(t, &output)
		logger.Info("string.bound", Field{Name: "stage", Value: strings.Repeat("a", 239) + "界tail"})
		record := decodeSingleRecord(t, output.Bytes())
		stage := record["stage"].(string)
		if !utf8.ValidString(stage) {
			t.Fatalf("stage is invalid UTF-8: %q", stage)
		}
		if len(stage) > 240 {
			t.Fatalf("stage is %d bytes, want <= 240", len(stage))
		}
		if stage != strings.Repeat("a", 239) {
			t.Fatalf("stage = %q", stage)
		}
	})

	t.Run("does not split a path escape at the byte cap", func(t *testing.T) {
		var output bytes.Buffer
		logger := newTestLogger(t, &output)
		logger.Info("path.bound", Field{Name: "path", Value: "/" + strings.Repeat("a", 237) + "%E7%95%8C"})
		record := decodeSingleRecord(t, output.Bytes())
		path := record["path"].(string)
		if len(path) > 240 {
			t.Fatalf("path is %d bytes, want <= 240", len(path))
		}
		if _, err := url.PathUnescape(path); err != nil {
			t.Fatalf("path ends in an incomplete escape: %q: %v", path, err)
		}
		if strings.HasSuffix(path, "%") || strings.HasSuffix(path, "%E") {
			t.Fatalf("path ends in a partial escape: %q", path)
		}
	})

	rejected := []any{
		[]string{"nested"},
		map[string]string{"nested": "value"},
		struct{ Value string }{Value: "nested"},
		1.5,
		float32(2.5),
		complex(1, 2),
		(*string)(nil),
	}
	for _, value := range rejected {
		t.Run(typeName(value), func(t *testing.T) {
			var output bytes.Buffer
			logger := newTestLogger(t, &output)
			logger.Info("value.rejected", Field{Name: "stage", Value: value})
			if output.Len() != 0 {
				t.Fatalf("value of type %T produced %q", value, output.String())
			}
		})
	}
}

func TestJSONLoggerRejectsSemanticScalarValuesBeforeReflection(t *testing.T) {
	pointerError := pointerErrorString("pointer-error-value")
	pointerStringer := pointerStringerInt(17)
	rejected := []any{
		errorString("named-error-value"),
		pointerErrorString("pointer-method-on-value"),
		&pointerError,
		stringerString("named-stringer-value"),
		pointerStringerInt(13),
		&pointerStringer,
	}
	for _, value := range rejected {
		t.Run(typeName(value), func(t *testing.T) {
			var output bytes.Buffer
			logger := newTestLogger(t, &output)
			logger.Info("semantic.rejected", Field{Name: "stage", Value: value})
			if output.Len() != 0 {
				t.Fatalf("semantic scalar %T produced %q", value, output.String())
			}
		})
	}

	t.Run("harmless named scalars remain accepted", func(t *testing.T) {
		var output bytes.Buffer
		logger := newTestLogger(t, &output)
		logger.Info("semantic.accepted",
			Field{Name: "stage", Value: harmlessString("named-scalar")},
			Field{Name: "count", Value: harmlessInt(3)},
			Field{Name: "state", Value: harmlessBool(true)},
		)
		decodeSingleRecord(t, output.Bytes())
	})
}

func TestJSONLoggerRepairsInvalidUTF8BeforeBounding(t *testing.T) {
	var output bytes.Buffer
	logger := newTestLogger(t, &output)
	invalid := string(append([]byte{0xff, 0xfe}, bytes.Repeat([]byte("界"), 100)...))
	logger.Info("utf8.repair", Field{Name: "stage", Value: invalid})

	record := decodeSingleRecord(t, output.Bytes())
	stage := record["stage"].(string)
	if !utf8.ValidString(stage) {
		t.Fatalf("stage is invalid UTF-8: %q", stage)
	}
	if len(stage) > 240 {
		t.Fatalf("stage is %d bytes, want <= 240", len(stage))
	}
	if !strings.HasPrefix(stage, "�") {
		t.Fatalf("stage prefix = %q, want a replacement rune", stage)
	}
}

func TestJSONLoggerAcceptsBoundedIntegers(t *testing.T) {
	type signedAlias int64
	type unsignedAlias uint64

	accepted := []any{
		int(math.MinInt), int8(math.MinInt8), int16(math.MinInt16), int32(math.MinInt32), int64(math.MinInt64),
		uint(0), uint8(math.MaxUint8), uint16(math.MaxUint16), uint32(math.MaxUint32),
		uint64(math.MaxInt64), signedAlias(math.MaxInt64), unsignedAlias(math.MaxInt64),
	}
	for _, value := range accepted {
		t.Run(typeName(value), func(t *testing.T) {
			var output bytes.Buffer
			logger := newTestLogger(t, &output)
			logger.Info("integer.accepted", Field{Name: "count", Value: value})
			if output.Len() == 0 {
				t.Fatalf("integer %T(%v) was rejected", value, value)
			}
			decodeSingleRecord(t, output.Bytes())
		})
	}

	rejected := []any{uint64(math.MaxInt64) + 1, ^uint(0), uintptr(1)}
	for _, value := range rejected {
		t.Run(typeName(value), func(t *testing.T) {
			var output bytes.Buffer
			logger := newTestLogger(t, &output)
			logger.Info("integer.rejected", Field{Name: "count", Value: value})
			if output.Len() != 0 {
				t.Fatalf("integer %T(%v) produced %q", value, value, output.String())
			}
		})
	}
}

func TestJSONLoggerRejectsDuplicateAndReservedFields(t *testing.T) {
	tests := []struct {
		name   string
		fields []Field
	}{
		{
			name: "duplicate",
			fields: []Field{
				{Name: "stage", Value: "first"},
				{Name: "stage", Value: "second"},
			},
		},
		{name: "timestamp", fields: []Field{{Name: "timestamp", Value: "attacker"}}},
		{name: "level", fields: []Field{{Name: "level", Value: "attacker"}}},
		{name: "event", fields: []Field{{Name: "event", Value: "attacker"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := newTestLogger(t, &output)
			logger.Info("field.rejected", test.fields...)
			if output.Len() != 0 {
				t.Fatalf("invalid fields produced %q", output.String())
			}
		})
	}
}

func TestJSONLoggerRejectsInvalidEvents(t *testing.T) {
	events := []string{
		"",
		"Uppercase",
		"9starts_with_digit",
		"contains space",
		"line\ninjection",
		strings.Repeat("a", 65),
		"事件",
		string([]byte{0xff}),
	}
	for _, event := range events {
		t.Run(typeName(event), func(t *testing.T) {
			var output bytes.Buffer
			logger := newTestLogger(t, &output)
			logger.Info(event, Field{Name: "stage", Value: "ignored"})
			if output.Len() != 0 {
				t.Fatalf("invalid event produced %q", output.String())
			}
		})
	}
}

func TestJSONLoggerRejectsNewlineJSONInjection(t *testing.T) {
	var output bytes.Buffer
	logger := newTestLogger(t, &output)
	logger.Info("json.injection", Field{Name: "stage", Value: "first\n{\"level\":\"error\"}\rsecond"})

	if output.Len() != 0 {
		t.Fatalf("newline injection produced %q", output.String())
	}
}

func TestJSONLoggerWriterFailureIsBestEffortWithoutSecretLeak(t *testing.T) {
	const secret = "writer-failure-secret"
	writer := &recordingFailureWriter{}
	logger, err := New(writer, func() time.Time { return fixedTime }, secret)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logger.Info("writer.failure", Field{Name: "stage", Value: "prefix-" + secret + "-suffix"})
	logger.Error("writer.failure", Field{Name: "stage", Value: "retry-is-a-new-call"})

	if writer.calls != 2 {
		t.Fatalf("Write calls = %d, want one per logging call", writer.calls)
	}
	if strings.Contains(writer.seen.String(), secret) {
		t.Fatalf("failed writer observed secret: %q", writer.seen.String())
	}
}

func TestJSONLoggerConcurrentWritesDoNotInterleave(t *testing.T) {
	const goroutines = 16
	const each = 15

	writer := &slowByteWriter{}
	logger := newTestLogger(t, writer)
	loggerCopy := logger
	var wait sync.WaitGroup
	for goroutine := 0; goroutine < goroutines; goroutine++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			selected := logger
			if worker%2 == 0 {
				selected = loggerCopy
			}
			for iteration := 0; iteration < each; iteration++ {
				selected.Info("concurrent.write",
					Field{Name: "service", Value: "worker"},
					Field{Name: "count", Value: worker*each + iteration},
				)
			}
		}(goroutine)
	}
	wait.Wait()

	lines := bytes.Split(bytes.TrimSpace(writer.Bytes()), []byte("\n"))
	if len(lines) != goroutines*each {
		t.Fatalf("lines = %d, want %d", len(lines), goroutines*each)
	}
	for index, line := range lines {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("line %d is interleaved or invalid: %v: %q", index, err, line)
		}
		if record["event"] != "concurrent.write" {
			t.Fatalf("line %d event = %#v", index, record["event"])
		}
	}
}

func TestJSONLoggerRejectsNilDependenciesAndZeroLoggerIsSafe(t *testing.T) {
	if _, err := New(nil, func() time.Time { return fixedTime }); err == nil {
		t.Fatal("New accepted nil output")
	}
	if _, err := New(io.Discard, nil); err == nil {
		t.Fatal("New accepted nil clock")
	}

	var logger Logger
	logger.Info("zero.logger", Field{Name: "stage", Value: "ignored"})
	logger.Error("zero.logger", Field{Name: "stage", Value: "ignored"})
}

func newTestLogger(t *testing.T, output io.Writer) Logger {
	t.Helper()
	logger, err := New(output, func() time.Time { return fixedTime })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return logger
}

func decodeSingleRecord(t *testing.T, output []byte) map[string]any {
	t.Helper()
	if len(output) == 0 || output[len(output)-1] != '\n' {
		t.Fatalf("output is not newline terminated: %q", output)
	}
	if bytes.Count(output, []byte("\n")) != 1 {
		t.Fatalf("output has multiple physical lines: %q", output)
	}
	var record map[string]any
	decoder := json.NewDecoder(bytes.NewReader(output))
	if err := decoder.Decode(&record); err != nil {
		t.Fatalf("decode output: %v: %q", err, output)
	}
	return record
}

func typeName(value any) string {
	return strings.NewReplacer(
		"/", "_",
		" ", "_",
		"\n", "_",
		"\r", "_",
	).Replace(fmt.Sprintf("%T_%v", value, value))
}

type recordingFailureWriter struct {
	calls int
	seen  bytes.Buffer
}

func (writer *recordingFailureWriter) Write(value []byte) (int, error) {
	writer.calls++
	written := len(value) / 2
	_, _ = writer.seen.Write(value[:written])
	return written, errors.New("synthetic write failure")
}

type slowByteWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (writer *slowByteWriter) Write(value []byte) (int, error) {
	for _, current := range value {
		time.Sleep(time.Microsecond)
		writer.mu.Lock()
		_ = writer.buffer.WriteByte(current)
		writer.mu.Unlock()
	}
	return len(value), nil
}

func (writer *slowByteWriter) Bytes() []byte {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return bytes.Clone(writer.buffer.Bytes())
}

type errorString string

func (value errorString) Error() string {
	return string(value)
}

type pointerErrorString string

func (value *pointerErrorString) Error() string {
	return string(*value)
}

type stringerString string

func (value stringerString) String() string {
	return string(value)
}

type pointerStringerInt int

func (value *pointerStringerInt) String() string {
	return fmt.Sprintf("%d", *value)
}

type harmlessString string
type harmlessInt int64
type harmlessBool bool
