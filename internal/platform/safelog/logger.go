// Package safelog writes bounded, structured operational logs.
//
// Logging is deliberately fail closed: a record with an invalid event, field
// name, or field value is discarded in full. Writes are best effort. Each
// valid record is passed to the configured writer exactly once, and write
// errors or short writes are neither retried nor buffered.
package safelog

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxEventBytes  = 64
	maxStringBytes = 240
	redactedValue  = "redacted"
)

var (
	allowedFields = map[string]struct{}{
		"trace_id":    {},
		"request_id":  {},
		"stage":       {},
		"category":    {},
		"method":      {},
		"path":        {},
		"status":      {},
		"duration_ms": {},
		"service":     {},
		"state":       {},
		"count":       {},
		"bytes":       {},
	}
	forbiddenFieldFragments = []string{
		"secret",
		"token",
		"password",
		"cookie",
		"authorization",
		"credential",
		"key",
		"url",
		"query",
		"body",
		"content",
	}
)

// Field is a single allowlisted structured log field.
type Field struct {
	Name  string
	Value any
}

// Logger writes privacy-bounded JSON records. A zero Logger is a safe no-op.
//
// Logger values may be copied. Copies share the writer serialization lock and
// redaction configuration.
type Logger struct {
	state *loggerState
}

type loggerState struct {
	mu       sync.Mutex
	output   io.Writer
	clock    func() time.Time
	redactor redactor
}

type redactor struct {
	markers []string
}

// New constructs a structured logger. Secret markers must each contain at
// least eight bytes. The logger owns an independent copy of the marker slice.
func New(output io.Writer, clock func() time.Time, secretValues ...string) (Logger, error) {
	if nilInterface(output) {
		return Logger{}, errors.New("safelog: output is required")
	}
	if clock == nil {
		return Logger{}, errors.New("safelog: clock is required")
	}

	markers := make([]string, len(secretValues))
	for index, marker := range secretValues {
		if len(marker) < 8 {
			return Logger{}, errors.New("safelog: secret marker must contain at least eight bytes")
		}
		markers[index] = strings.Clone(marker)
	}
	sort.Slice(markers, func(left, right int) bool {
		if len(markers[left]) == len(markers[right]) {
			return markers[left] < markers[right]
		}
		return len(markers[left]) > len(markers[right])
	})

	return Logger{state: &loggerState{
		output:   output,
		clock:    clock,
		redactor: redactor{markers: markers},
	}}, nil
}

// Info writes an informational record when the event and every field are
// valid. Invalid records are discarded.
func (logger Logger) Info(event string, fields ...Field) {
	logger.write("info", event, fields)
}

// Error writes an error-level record when the event and every field are
// valid. Invalid records are discarded.
func (logger Logger) Error(event string, fields ...Field) {
	logger.write("error", event, fields)
}

func (logger Logger) write(level, event string, fields []Field) {
	if logger.state == nil {
		return
	}

	logger.state.mu.Lock()
	defer logger.state.mu.Unlock()

	if !validEvent(event) {
		return
	}

	record := make(map[string]any, len(fields)+3)
	record["timestamp"] = logger.state.redactor.render(logger.state.clock().Format(time.RFC3339Nano))
	record["level"] = level
	record["event"] = logger.state.redactor.render(event)

	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if !validFieldName(field.Name) {
			return
		}
		if _, duplicate := seen[field.Name]; duplicate {
			return
		}
		seen[field.Name] = struct{}{}

		value, ok := logger.state.redactor.normalizeField(field.Name, field.Value)
		if !ok {
			return
		}
		record[field.Name] = value
	}

	encoded, err := json.Marshal(record)
	if err != nil {
		return
	}
	encoded = append(encoded, '\n')
	written, err := logger.state.output.Write(encoded)
	if err != nil || written != len(encoded) {
		return
	}
}

func validEvent(event string) bool {
	if len(event) == 0 || len(event) > maxEventBytes || !utf8.ValidString(event) {
		return false
	}
	for index := 0; index < len(event); index++ {
		current := event[index]
		if index == 0 {
			if current < 'a' || current > 'z' {
				return false
			}
			continue
		}
		if current >= 'a' && current <= 'z' {
			continue
		}
		if current >= '0' && current <= '9' {
			continue
		}
		switch current {
		case '.', '_', '-':
			continue
		default:
			return false
		}
	}
	return true
}

func validFieldName(name string) bool {
	lower := strings.ToLower(name)
	for _, fragment := range forbiddenFieldFragments {
		if strings.Contains(lower, fragment) {
			return false
		}
	}
	_, ok := allowedFields[name]
	return ok
}

func (redactor redactor) normalizeField(name string, value any) (any, bool) {
	if value == nil {
		return nil, false
	}
	if duration, ok := value.(time.Duration); ok {
		return duration.Milliseconds(), true
	}

	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.String:
		raw := reflected.String()
		if name == "path" {
			if !validEscapedPath(raw) {
				return nil, false
			}
			return redactor.renderPath(raw), true
		}
		rendered := redactor.render(raw)
		if !validSafeString(rendered) {
			return nil, false
		}
		return rendered, true
	case reflect.Bool:
		return reflected.Bool(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		unsigned := reflected.Uint()
		if unsigned > math.MaxInt64 {
			return nil, false
		}
		return unsigned, true
	default:
		return nil, false
	}
}

func validEscapedPath(value string) bool {
	if value == "" || value[0] != '/' || strings.ContainsAny(value, "?#") {
		return false
	}
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return false
	}
	parsed := &url.URL{Path: decoded, RawPath: value}
	return parsed.EscapedPath() == value
}

func validSafeString(value string) bool {
	if value == "" {
		return false
	}
	for _, current := range value {
		if unicode.IsLetter(current) || unicode.IsNumber(current) || current == utf8.RuneError {
			continue
		}
		switch current {
		case '.', '_', '-':
			continue
		default:
			return false
		}
	}
	return true
}

func (redactor redactor) render(value string) string {
	value = redactor.redactAndRepair(value)
	return boundUTF8(value)
}

func (redactor redactor) renderPath(value string) string {
	value = redactor.redactAndRepair(value)
	if len(value) <= maxStringBytes {
		return value
	}

	for end := maxStringBytes; end > 0; end-- {
		candidate := value[:end]
		if utf8.ValidString(candidate) && validEscapedPath(candidate) {
			return candidate
		}
	}
	return "/"
}

func (redactor redactor) redactAndRepair(value string) string {
	for _, marker := range redactor.markers {
		value = strings.ReplaceAll(value, marker, redactedValue)
	}
	return strings.ToValidUTF8(value, "\uFFFD")
}

func boundUTF8(value string) string {
	if len(value) <= maxStringBytes {
		return value
	}

	end := maxStringBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
