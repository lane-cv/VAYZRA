package updates

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
				_, _ = w.Write([]byte(`{"enabled":true,"state":"current","strategy":"github-release","repository":"https://github.com/example/project.git","ref":"master","channel":"stable","currentVersion":"1.2.3","latestVersion":"1.2.3","currentCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","latestCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","releaseName":"v1.2.3","releaseNotes":"notes","releaseURL":"https://github.com/example/project/releases/tag/v1.2.3","publishedAt":"2026-08-12T10:30:00Z","updateAvailable":false,"dirty":false,"canRollback":false,"previousVersion":"","phase":"complete","progress":100,"message":"current","startedAt":null,"finishedAt":null}`))
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

func TestAgentClientCallsRollbackAndMapsUnavailable(t *testing.T) {
	const token = "test-update-agent-token-with-at-least-32-chars"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rollback" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	client, err := NewAgentClient(server.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Rollback(context.Background()); err != ErrRollbackUnavailable {
		t.Fatalf("rollback error = %v", err)
	}
}

func TestAgentClientAcceptsMaximumReleaseNotes(t *testing.T) {
	status := validAgentStatusFixture()
	status.ReleaseNotes = strings.Repeat("n", 32*1024)
	payload, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	client := agentClientReturning(t, payload)
	got, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.ReleaseNotes != status.ReleaseNotes {
		t.Fatalf("release notes length = %d, want %d", len(got.ReleaseNotes), len(status.ReleaseNotes))
	}
}

func TestAgentClientRejectsNonCanonicalJSON(t *testing.T) {
	status := validAgentStatusFixture()
	payload, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	unknown := append(append([]byte(nil), payload[:len(payload)-1]...), []byte(`,"unexpected":true}`)...)
	missing := []byte(strings.Replace(string(payload), `"releaseNotes":"",`, "", 1))
	duplicate := append([]byte(`{"enabled":true,`), payload[1:]...)
	tests := map[string][]byte{
		"unknown field":      unknown,
		"missing field":      missing,
		"duplicate field":    duplicate,
		"trailing object":    append(append([]byte(nil), payload...), []byte(`{}`)...),
		"oversized response": append(append([]byte(nil), payload...), []byte(strings.Repeat(" ", 64*1024))...),
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			client := agentClientReturning(t, response)
			if _, err := client.Status(context.Background()); err != ErrAgentUnavailable {
				t.Fatalf("Status() error = %v, want %v", err, ErrAgentUnavailable)
			}
		})
	}
}

func TestAgentClientMapsExactLegacyStatusToBlocked(t *testing.T) {
	for name, payload := range map[string][]byte{
		"checked": []byte(`{"enabled":true,"state":"available","repository":"https://github.com/example/project.git","ref":"master","currentCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","latestCommit":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","updateAvailable":true,"dirty":false,"message":"发现新的 GitHub 提交","startedAt":null,"finishedAt":null}`),
		"initial": []byte(`{"enabled":true,"state":"unknown","repository":"","ref":"master","currentCommit":"","latestCommit":"","updateAvailable":false,"dirty":false,"message":"尚未检查 GitHub 更新","startedAt":null,"finishedAt":null}`),
	} {
		t.Run(name, func(t *testing.T) {
			client := agentClientReturning(t, payload)
			for callName, call := range map[string]func(context.Context) (Status, error){
				"status": client.Status,
				"check":  client.Check,
			} {
				t.Run(callName, func(t *testing.T) {
					status, err := call(context.Background())
					if err != nil {
						t.Fatalf("call error = %v", err)
					}
					if !status.LegacyProtocol || status.State != StateBlocked || status.UpdateAvailable ||
						status.Strategy != StrategyGitHubRelease || status.Channel != ChannelStable ||
						status.Phase != PhaseComplete || status.Progress != 100 {
						t.Fatalf("legacy status = %+v", status)
					}
				})
			}
		})
	}
}

func TestAgentClientDoesNotPostApplyToLegacyAgent(t *testing.T) {
	const payload = `{"enabled":true,"state":"available","repository":"https://github.com/example/project.git","ref":"master","currentCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","latestCommit":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","updateAvailable":true,"dirty":false,"message":"available","startedAt":null,"finishedAt":null}`
	applyCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/apply" {
			applyCalls++
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()
	client, err := NewAgentClient(server.URL, "test-update-agent-token-with-at-least-32-chars")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Apply(context.Background()); err != ErrAgentProtocolOutdated {
		t.Fatalf("Apply() error = %v", err)
	}
	if applyCalls != 0 {
		t.Fatalf("legacy apply POST calls = %d", applyCalls)
	}
}

func TestAgentClientRejectsInexactLegacyStatus(t *testing.T) {
	const payload = `{"enabled":true,"state":"available","repository":"https://github.com/example/project.git","ref":"master","currentCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","latestCommit":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","updateAvailable":true,"dirty":false,"message":"available","startedAt":null,"finishedAt":null}`
	for name, response := range map[string][]byte{
		"missing field":   []byte(strings.Replace(payload, `"message":"available",`, "", 1)),
		"duplicate field": []byte(strings.Replace(payload, `{"enabled":true,`, `{"enabled":true,"enabled":true,`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			client := agentClientReturning(t, response)
			if _, err := client.Status(context.Background()); err != ErrAgentUnavailable {
				t.Fatalf("Status() error = %v", err)
			}
		})
	}
}

func validAgentStatusFixture() Status {
	published := time.Date(2026, 8, 12, 10, 30, 0, 0, time.UTC)
	return Status{
		Enabled: true, State: StateCurrent, Strategy: StrategyGitHubRelease,
		Repository: "https://github.com/example/project.git", Ref: "master", Channel: ChannelStable,
		CurrentVersion: "1.2.3", LatestVersion: "1.2.3",
		CurrentCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		LatestCommit:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ReleaseName:   "v1.2.3", ReleaseURL: "https://github.com/example/project/releases/tag/v1.2.3",
		PublishedAt: &published, Phase: PhaseComplete, Progress: 100, Message: "current",
	}
}

func agentClientReturning(t *testing.T, payload []byte) *AgentClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	client, err := NewAgentClient(server.URL, "test-update-agent-token-with-at-least-32-chars")
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestNewAgentClientRejectsUnsafeURL(t *testing.T) {
	for _, baseURL := range []string{"https://agent.example", "http://agent.example/path", "http://agent.example/path?x=1", "http://user:pass@agent.example"} {
		if _, err := NewAgentClient(baseURL, "token"); err != ErrInvalid {
			t.Fatalf("baseURL %q error = %v", baseURL, err)
		}
	}
	for _, token := range []string{"", "line\nbreak", strings.Repeat("x", 4097)} {
		if _, err := NewAgentClient("http://agent.example", token); err != ErrInvalid {
			t.Fatalf("token length %d error = %v", len(token), err)
		}
	}
}

func TestValidStatusRejectsIncoherentOrNonCanonicalFields(t *testing.T) {
	published := time.Date(2026, 8, 12, 10, 30, 0, 0, time.UTC)
	base := Status{
		Enabled: true, State: StateAvailable, Strategy: StrategyGitHubRelease,
		Repository: "https://github.com/example/project.git", Ref: "master", Channel: ChannelStable,
		CurrentVersion: "1.2.2", LatestVersion: "1.2.3",
		CurrentCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		LatestCommit:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ReleaseName:   "v1.2.3", ReleaseURL: "https://github.com/example/project/releases/tag/v1.2.3",
		PublishedAt: &published, UpdateAvailable: true, Phase: PhaseComplete, Progress: 100,
	}
	if !validStatus(base) {
		t.Fatal("valid status rejected")
	}
	for name, mutate := range map[string]func(*Status){
		"credentialed repository":  func(status *Status) { status.Repository = "https://user:secret@github.com/example/project.git" },
		"owner with underscore":    func(status *Status) { status.Repository = "https://github.com/invalid_owner/project.git" },
		"owner with edge hyphen":   func(status *Status) { status.Repository = "https://github.com/-invalid/project.git" },
		"foreign release":          func(status *Status) { status.ReleaseURL = "https://github.com/attacker/project/releases/tag/v1.2.3" },
		"rollback advertised":      func(status *Status) { status.CanRollback = true },
		"available progress":       func(status *Status) { status.Progress = 50 },
		"release without date":     func(status *Status) { status.PublishedAt = nil },
		"release version mismatch": func(status *Status) { status.LatestVersion = "1.2.4" },
		"available flag mismatch":  func(status *Status) { status.UpdateAvailable = false },
	} {
		t.Run(name, func(t *testing.T) {
			status := base
			mutate(&status)
			if validStatus(status) {
				t.Fatalf("invalid status accepted: %+v", status)
			}
		})
	}
	disabled := Status{State: StateDisabled, Phase: PhaseIdle, Message: "disabled"}
	if !validStatus(disabled) {
		t.Fatal("canonical disabled status rejected")
	}
	disabled.Repository = base.Repository
	if validStatus(disabled) {
		t.Fatal("disabled status leaked repository shape")
	}
}
