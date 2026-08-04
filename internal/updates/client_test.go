package updates

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentClientReadsStatusAndMapsDirtyCheckout(t *testing.T) {
	const token = "test-update-agent-token-with-at-least-32-chars"
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &httptest.Server{
		Listener: listener,
		Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+token {
				t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
			}
			if r.URL.Path == "/v1/status" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"enabled":true,"state":"current","repository":"https://github.com/example/project.git","ref":"master","currentCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","latestCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","updateAvailable":false,"dirty":false,"message":"current","startedAt":null,"finishedAt":null}`))
				return
			}
			w.WriteHeader(http.StatusPreconditionFailed)
		})},
	}
	server.Start()
	defer server.Close()

	client, err := NewAgentClient(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(context.Background())
	if err != nil || status.State != StateCurrent {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if _, err := client.Apply(context.Background()); err != ErrDirtyCheckout {
		t.Fatalf("apply error = %v", err)
	}
}

func TestNewAgentClientRejectsUnsafeURL(t *testing.T) {
	for _, baseURL := range []string{"https://agent.example", "http://agent.example/path?x=1", "http://user:pass@agent.example"} {
		if _, err := NewAgentClient(baseURL, "token"); err != ErrInvalid {
			t.Fatalf("baseURL %q error = %v", baseURL, err)
		}
	}
}
