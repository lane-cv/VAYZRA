package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSelectLatestStableReleaseSkipsUnsafeEntries(t *testing.T) {
	published := "2026-08-12T10:30:00Z"
	releases := []githubRelease{
		{TagName: "nightly", Name: "nightly", PublishedAt: published},
		{TagName: "v2.0.0-rc.1", Name: "candidate", PublishedAt: published, Prerelease: true},
		{TagName: "v9.0.0", Name: "draft", PublishedAt: published, Draft: true},
		{TagName: "v1.9.0", Name: "older", PublishedAt: published, Immutable: true},
		{TagName: "v1.10.0", Name: "stable", Body: "release notes", PublishedAt: published, Immutable: true},
		{TagName: "v99.0.0", Name: "mutable", PublishedAt: published},
	}

	release, err := selectLatestStableRelease(releases, githubRepository{Owner: "lane-cv", Name: "VAYZRA"})
	if err != nil {
		t.Fatal(err)
	}
	if release.Tag != "v1.10.0" || release.Version != "1.10.0" || release.Name != "stable" {
		t.Fatalf("release = %+v", release)
	}
	if release.URL != "https://github.com/lane-cv/VAYZRA/releases/tag/v1.10.0" {
		t.Fatalf("release URL = %q", release.URL)
	}
	wantPublished, _ := time.Parse(time.RFC3339, published)
	if !release.PublishedAt.Equal(wantPublished) {
		t.Fatalf("publishedAt = %s", release.PublishedAt)
	}
}

func TestParseStableSemanticVersionIsStrict(t *testing.T) {
	for _, valid := range []string{"v0.0.1", "v1.2.3", "v12.34.56"} {
		if _, ok := parseStableSemanticVersion(valid); !ok {
			t.Errorf("valid tag %q rejected", valid)
		}
	}
	for _, invalid := range []string{"", "v1", "1.2", "1.2.3", "v01.2.3", "v1.2.3-rc.1", "v1.2.3+build.7", "v1.2.3/../../main", strings.Repeat("1", 129)} {
		if _, ok := parseStableSemanticVersion(invalid); ok {
			t.Errorf("invalid tag %q accepted", invalid)
		}
	}
}

func TestParseNormalizedSemanticVersionAcceptsOnlyCanonicalStatusValue(t *testing.T) {
	for _, valid := range []string{"0.0.1", "1.2.3", "12.34.56"} {
		if _, ok := parseNormalizedSemanticVersion(valid); !ok {
			t.Errorf("valid normalized version %q rejected", valid)
		}
	}
	for _, invalid := range []string{"", "v1.2.3", "01.2.3", "1.2.3-rc.1"} {
		if _, ok := parseNormalizedSemanticVersion(invalid); ok {
			t.Errorf("invalid normalized version %q accepted", invalid)
		}
	}
}

func TestParseGitHubRepositoryAcceptsOnlyCanonicalGitHubRemotes(t *testing.T) {
	for _, raw := range []string{
		"git@github.com:lane-cv/VAYZRA.git",
		"ssh://git@github.com/lane-cv/VAYZRA.git",
		"https://github.com/lane-cv/VAYZRA.git",
	} {
		repository, err := parseGitHubRepository(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if repository.Owner != "lane-cv" || repository.Name != "VAYZRA" {
			t.Fatalf("repository for %q = %+v", raw, repository)
		}
	}
	for _, raw := range []string{
		"https://github.example/lane-cv/VAYZRA.git",
		"https://user:pass@github.com/lane-cv/VAYZRA.git",
		"https://github.com/lane-cv/VAYZRA.git?token=secret",
		"https://github.com/lane-cv/VAYZRA/extra",
		"file:///workspace",
	} {
		if _, err := parseGitHubRepository(raw); err == nil {
			t.Errorf("unsafe repository %q accepted", raw)
		}
	}
}

type releaseRoundTripper func(*http.Request) (*http.Response, error)

func (f releaseRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestGitHubReleaseRequestScopesTokenToAPIHost(t *testing.T) {
	const token = "github-secret-marker-0123456789"
	client := &githubReleaseClient{client: &http.Client{Transport: releaseRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != "api.github.com" {
			t.Fatalf("request URL = %s", request.URL)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+token {
			t.Fatalf("Authorization = %q", got)
		}
		body := `[{"tag_name":"v1.2.3","name":"release","body":"notes","html_url":"https://attacker.invalid/leak","published_at":"2026-08-12T10:30:00Z","draft":false,"prerelease":false,"immutable":true}]`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}, token: token}

	release, err := client.latest(context.Background(), githubRepository{Owner: "lane-cv", Name: "VAYZRA"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(release.URL, "attacker") {
		t.Fatalf("untrusted release URL escaped: %q", release.URL)
	}
}

func TestGitHubReleasePaginationFindsHighestStableVersion(t *testing.T) {
	requests := 0
	client := &githubReleaseClient{client: &http.Client{Transport: releaseRoundTripper(func(request *http.Request) (*http.Response, error) {
		requests++
		header := make(http.Header)
		body := `[{"tag_name":"v1.0.0","name":"first","published_at":"2026-08-12T10:30:00Z","immutable":true}]`
		if requests == 1 {
			header.Set("Link", `<https://api.github.com/repos/lane-cv/VAYZRA/releases?per_page=30&page=2>; rel="next"`)
		} else {
			if request.URL.String() != "https://api.github.com/repos/lane-cv/VAYZRA/releases?per_page=30&page=2" {
				t.Fatalf("second page URL = %s", request.URL)
			}
			body = `[{"tag_name":"v2.0.0","name":"second","published_at":"2026-08-12T11:30:00Z","immutable":true}]`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}

	release, err := client.latest(context.Background(), githubRepository{Owner: "lane-cv", Name: "VAYZRA"})
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "2.0.0" || requests != 2 {
		t.Fatalf("release=%+v requests=%d", release, requests)
	}
}

func TestGitHubReleasePaginationRejectsUntrustedNextLink(t *testing.T) {
	client := &githubReleaseClient{client: &http.Client{Transport: releaseRoundTripper(func(*http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Link", `<https://attacker.invalid/releases?page=2>; rel="next"`)
		body := `[{"tag_name":"v1.0.0","name":"first","published_at":"2026-08-12T10:30:00Z","immutable":true}]`
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	if _, err := client.latest(context.Background(), githubRepository{Owner: "lane-cv", Name: "VAYZRA"}); err == nil {
		t.Fatal("untrusted pagination link accepted")
	}
}

func TestGitHubReleaseCommitRequiresLatestSuccessfulVerifyPush(t *testing.T) {
	commit := strings.Repeat("a", 40)
	client := &githubReleaseClient{client: &http.Client{Transport: releaseRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != "api.github.com" ||
			request.URL.Path != "/repos/lane-cv/VAYZRA/actions/workflows/verify.yml/runs" {
			t.Fatalf("verification URL = %s", request.URL)
		}
		query := request.URL.Query()
		if query.Get("branch") != "master" || query.Get("event") != "push" ||
			query.Get("head_sha") != commit || query.Get("per_page") != "1" {
			t.Fatalf("verification query = %v", query)
		}
		body := `{"total_count":1,"workflow_runs":[{"head_sha":"` + commit + `","head_branch":"master","event":"push","status":"completed","conclusion":"success"}]}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	if err := client.verifyCommit(context.Background(), githubRepository{Owner: "lane-cv", Name: "VAYZRA"}, "master", commit); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubReleaseCommitRejectsFailedOrMismatchedVerifyRun(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for name, body := range map[string]string{
		"latest failed": `{"total_count":1,"workflow_runs":[{"head_sha":"` + commit + `","head_branch":"master","event":"push","status":"completed","conclusion":"failure"}]}`,
		"wrong commit":  `{"total_count":1,"workflow_runs":[{"head_sha":"` + strings.Repeat("b", 40) + `","head_branch":"master","event":"push","status":"completed","conclusion":"success"}]}`,
		"no run":        `{"total_count":0,"workflow_runs":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			client := &githubReleaseClient{client: &http.Client{Transport: releaseRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			if err := client.verifyCommit(context.Background(), githubRepository{Owner: "lane-cv", Name: "VAYZRA"}, "master", commit); err == nil {
				t.Fatal("unverified Release commit accepted")
			}
		})
	}
}
