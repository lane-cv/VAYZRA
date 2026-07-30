package httpx

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"happylearn.local/app/internal/platform/safelog"
)

func TestSafeServerErrorLogDropsRawMessages(t *testing.T) {
	const secret = "http-server-runtime-secret"
	var output bytes.Buffer
	logger, err := safelog.New(&output, time.Now, secret)
	if err != nil {
		t.Fatalf("safelog.New: %v", err)
	}

	SafeServerErrorLog(logger, "public").Print(secret)

	if bytes.Contains(output.Bytes(), []byte(secret)) {
		t.Fatalf("server error log leaked raw message: %q", output.Bytes())
	}
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode record %q: %v", output.Bytes(), err)
	}
	if record["event"] != "http.server.error" ||
		record["category"] != "runtime" ||
		record["service"] != "public" {
		t.Fatalf("record = %#v", record)
	}
}
