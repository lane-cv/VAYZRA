package main

import (
	"context"
	"encoding/base64"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type gitConfigEntry struct {
	key   string
	value string
}

func TestGitHubBasicAuthorization(t *testing.T) {
	const token = "github-test-token"
	header := githubBasicAuthorization(token)
	const prefix = "Authorization: Basic "
	if !strings.HasPrefix(header, prefix) {
		t.Fatalf("header = %q, want Basic authorization", header)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if got, want := string(decoded), "x-access-token:"+token; got != want {
		t.Fatalf("decoded credential = %q, want %q", got, want)
	}
	if strings.Contains(header, "Bearer") {
		t.Fatal("header unexpectedly uses Bearer authentication")
	}
}

func TestGitNetworkEnvironmentIsolatesGitConfiguration(t *testing.T) {
	const token = "github-secret-marker-0123456789"
	base := []string{
		"PATH=/usr/bin:/bin",
		"HTTPS_PROXY=http://proxy.example:8080",
		"SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt",
		"GIT_SSL_CAINFO=/certs/company-ca.pem",
		"GIT_SSL_CAPATH=/certs/company-ca",
		"GIT_CONFIG_COUNT=9",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Bearer stale-token",
		"GIT_CONFIG_PARAMETERS='http.extraHeader=stale-token'",
		"GIT_TRACE_CURL=1",
		"GIT_CURL_VERBOSE=1",
		"GIT_ASKPASS=/tmp/untrusted-helper",
		"SSH_ASKPASS=/tmp/untrusted-ssh-helper",
		"SSH_ASKPASS_REQUIRE=force",
		"GCM_INTERACTIVE=always",
	}
	original := append([]string(nil), base...)

	environment := gitNetworkEnvironment(base, token)
	if !reflect.DeepEqual(base, original) {
		t.Fatal("gitNetworkEnvironment modified its input")
	}
	values := environmentValues(t, environment)
	for name, want := range map[string]string{
		"PATH":                "/usr/bin:/bin",
		"HTTPS_PROXY":         "http://proxy.example:8080",
		"SSL_CERT_FILE":       "/etc/ssl/certs/ca-certificates.crt",
		"GIT_SSL_CAINFO":      "/certs/company-ca.pem",
		"GIT_SSL_CAPATH":      "/certs/company-ca",
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_ASKPASS":         "/bin/false",
		"GCM_INTERACTIVE":     "never",
	} {
		if got := values[name]; got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	for _, forbidden := range []string{
		"GIT_CONFIG_PARAMETERS",
		"GIT_TRACE_CURL",
		"GIT_CURL_VERBOSE",
		"SSH_ASKPASS",
		"SSH_ASKPASS_REQUIRE",
	} {
		if _, ok := values[forbidden]; ok {
			t.Fatalf("inherited %s was not removed", forbidden)
		}
	}

	config := gitConfiguration(t, values)
	assertConfigValues(t, config, "credential.helper", []string{""})
	assertConfigValues(t, config, "credential.interactive", []string{"false"})
	assertConfigValues(t, config, "http.extraHeader", []string{""})
	assertConfigValues(t, config, "http.https://github.com/.extraHeader", []string{"", githubBasicAuthorization(token)})
	for _, entry := range environment {
		if strings.Contains(entry, "stale-token") {
			t.Fatal("stale inherited credential remained in the environment")
		}
	}
}

func TestGitNetworkEnvironmentWithoutToken(t *testing.T) {
	environment := gitNetworkEnvironment([]string{"PATH=/usr/bin:/bin"}, "")
	values := environmentValues(t, environment)
	config := gitConfiguration(t, values)

	if got := values["GIT_TERMINAL_PROMPT"]; got != "0" {
		t.Fatalf("GIT_TERMINAL_PROMPT = %q, want 0", got)
	}
	if got := values["GIT_ASKPASS"]; got != "/bin/false" {
		t.Fatalf("GIT_ASKPASS = %q, want /bin/false", got)
	}
	assertConfigValues(t, config, "http.extraHeader", []string{""})
	assertConfigValues(t, config, "http.https://github.com/.extraHeader", []string{""})
	for _, entry := range config {
		if strings.Contains(entry.value, "Authorization:") {
			t.Fatalf("unexpected authorization config without token: %s=%q", entry.key, entry.value)
		}
	}
}

func TestGitNetworkCommandDoesNotExposeTokenInArguments(t *testing.T) {
	const token = "github-secret-marker-0123456789"
	agent := &agent{cfg: config{repository: "/workspace", githubToken: token}}
	encoded := strings.TrimPrefix(githubBasicAuthorization(token), "Authorization: Basic ")

	for name, args := range map[string][]string{
		"ls-remote": {"ls-remote", "https://github.com/lane-cv/VAYZRA.git", "refs/heads/master"},
		"fetch":     {"fetch", "--prune", "https://github.com/lane-cv/VAYZRA.git", "master"},
	} {
		t.Run(name, func(t *testing.T) {
			command := agent.gitNetworkCommand(context.Background(), args...)
			wantArgs := append([]string{"git", "-c", "safe.directory=/workspace", "-C", "/workspace"}, args...)
			if !reflect.DeepEqual(command.Args, wantArgs) {
				t.Fatalf("command args = %q, want %q", command.Args, wantArgs)
			}
			joined := strings.Join(command.Args, "\x00")
			if strings.Contains(joined, token) || strings.Contains(joined, encoded) {
				t.Fatal("GitHub credential was exposed in command arguments")
			}

			values := environmentValues(t, command.Env)
			config := gitConfiguration(t, values)
			assertConfigValues(t, config, "http.https://github.com/.extraHeader", []string{"", githubBasicAuthorization(token)})
		})
	}
}

func environmentValues(t *testing.T, environment []string) map[string]string {
	t.Helper()
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("invalid environment entry %q", entry)
		}
		if _, exists := values[name]; exists {
			t.Fatalf("duplicate environment variable %s", name)
		}
		values[name] = value
	}
	return values
}

func gitConfiguration(t *testing.T, environment map[string]string) []gitConfigEntry {
	t.Helper()
	count, err := strconv.Atoi(environment["GIT_CONFIG_COUNT"])
	if err != nil {
		t.Fatalf("invalid GIT_CONFIG_COUNT: %v", err)
	}
	config := make([]gitConfigEntry, 0, count)
	for index := 0; index < count; index++ {
		key, ok := environment["GIT_CONFIG_KEY_"+strconv.Itoa(index)]
		if !ok {
			t.Fatalf("missing GIT_CONFIG_KEY_%d", index)
		}
		value, ok := environment["GIT_CONFIG_VALUE_"+strconv.Itoa(index)]
		if !ok {
			t.Fatalf("missing GIT_CONFIG_VALUE_%d", index)
		}
		config = append(config, gitConfigEntry{key: key, value: value})
	}
	return config
}

func assertConfigValues(t *testing.T, config []gitConfigEntry, key string, want []string) {
	t.Helper()
	var got []string
	for _, entry := range config {
		if entry.key == key {
			got = append(got, entry.value)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config values for %s = %q, want %q", key, got, want)
	}
}
