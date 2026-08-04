package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGithubGitAuthorizationUsesBasicXAccessToken(t *testing.T) {
	got := githubGitAuthorization("test-token")
	if !strings.HasPrefix(got, "Authorization: Basic ") {
		t.Fatalf("authorization scheme = %q", got)
	}
	encoded := strings.TrimPrefix(got, "Authorization: Basic ")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode authorization: %v", err)
	}
	if string(decoded) != "x-access-token:test-token" {
		t.Fatalf("decoded authorization = %q", decoded)
	}
}
