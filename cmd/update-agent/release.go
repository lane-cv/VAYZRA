package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxReleaseResponse   = 1024 * 1024
	maxWorkflowResponse  = 128 * 1024
	maxReleaseCount      = 30
	maxReleasePages      = 4
	trustedReleaseBranch = "master"
)

var (
	stableVersionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	githubOwnerPattern   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	githubNamePattern    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
)

type githubRepository struct {
	Owner string
	Name  string
}

func (r githubRepository) canonicalURL() string {
	return "https://github.com/" + r.Owner + "/" + r.Name + ".git"
}

type semanticVersion struct {
	major      uint64
	minor      uint64
	patch      uint64
	normalized string
}

func (v semanticVersion) compare(other semanticVersion) int {
	for _, pair := range [][2]uint64{{v.major, other.major}, {v.minor, other.minor}, {v.patch, other.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}

type githubRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	Immutable   bool   `json:"immutable"`
}

type stableRelease struct {
	Tag         string
	Version     string
	Name        string
	Notes       string
	URL         string
	PublishedAt time.Time
}

type githubReleaseClient struct {
	client *http.Client
	token  string
}

type githubWorkflowRuns struct {
	TotalCount   int                 `json:"total_count"`
	WorkflowRuns []githubWorkflowRun `json:"workflow_runs"`
}

type githubWorkflowRun struct {
	HeadSHA    string `json:"head_sha"`
	HeadBranch string `json:"head_branch"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

func newGitHubReleaseClient(token string) *githubReleaseClient {
	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 3 || request.URL.Scheme != "https" || request.URL.Host != "api.github.com" {
				return errors.New("github API redirect rejected")
			}
			return nil
		},
	}
	return &githubReleaseClient{client: client, token: token}
}

func (c *githubReleaseClient) latest(ctx context.Context, repository githubRepository) (stableRelease, error) {
	if c == nil || c.client == nil || ctx == nil || !validGitHubRepository(repository) {
		return stableRelease{}, errors.New("invalid github release request")
	}
	endpoint := "https://api.github.com/repos/" + repository.Owner + "/" + repository.Name + "/releases?per_page=" + strconv.Itoa(maxReleaseCount)
	all := make([]githubRelease, 0, maxReleaseCount)
	for page := 0; endpoint != "" && page < maxReleasePages; page++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return stableRelease{}, errors.New("invalid github release request")
		}
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
		request.Header.Set("User-Agent", "happylearn-update-agent/1")
		if c.token != "" {
			request.Header.Set("Authorization", "Bearer "+c.token)
		}
		response, err := c.client.Do(request)
		if err != nil {
			return stableRelease{}, errors.New("github release request failed")
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxReleaseResponse+1))
		closeErr := response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return stableRelease{}, fmt.Errorf("github release request returned HTTP %d", response.StatusCode)
		}
		if readErr != nil || closeErr != nil || len(raw) > maxReleaseResponse {
			return stableRelease{}, errors.New("github release response is invalid")
		}
		var releases []githubRelease
		if err := json.Unmarshal(raw, &releases); err != nil || len(releases) > maxReleaseCount {
			return stableRelease{}, errors.New("github release response is invalid")
		}
		all = append(all, releases...)
		next, err := nextGitHubReleasePage(response.Header.Get("Link"), repository)
		if err != nil {
			return stableRelease{}, err
		}
		endpoint = next
	}
	if endpoint != "" {
		return stableRelease{}, errors.New("github release pagination limit exceeded")
	}
	return selectLatestStableRelease(all, repository)
}

func (c *githubReleaseClient) verifyCommit(ctx context.Context, repository githubRepository, branch, commit string) error {
	if c == nil || c.client == nil || ctx == nil || !validGitHubRepository(repository) ||
		!validBranchRef(branch) || !validCommit(commit) {
		return errors.New("invalid github workflow verification request")
	}
	query := url.Values{
		"branch":   {branch},
		"event":    {"push"},
		"head_sha": {commit},
		"per_page": {"1"},
	}
	endpoint := "https://api.github.com/repos/" + repository.Owner + "/" + repository.Name +
		"/actions/workflows/verify.yml/runs?" + query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("invalid github workflow verification request")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	request.Header.Set("User-Agent", "happylearn-update-agent/1")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return errors.New("github workflow verification request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("github workflow verification returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxWorkflowResponse+1))
	if err != nil || len(raw) > maxWorkflowResponse {
		return errors.New("github workflow verification response is invalid")
	}
	var runs githubWorkflowRuns
	if err := json.Unmarshal(raw, &runs); err != nil || runs.TotalCount < 1 || len(runs.WorkflowRuns) != 1 {
		return errors.New("release commit has no completed verification")
	}
	latest := runs.WorkflowRuns[0]
	if latest.HeadSHA != commit || latest.HeadBranch != branch || latest.Event != "push" ||
		latest.Status != "completed" || latest.Conclusion != "success" {
		return errors.New("release commit verification did not succeed")
	}
	return nil
}

func nextGitHubReleasePage(link string, repository githubRepository) (string, error) {
	if strings.TrimSpace(link) == "" {
		return "", nil
	}
	for _, part := range strings.Split(link, ",") {
		sections := strings.Split(strings.TrimSpace(part), ";")
		if len(sections) < 2 || !strings.Contains(strings.Join(sections[1:], ";"), `rel="next"`) {
			continue
		}
		raw := strings.TrimSpace(sections[0])
		if len(raw) < 3 || raw[0] != '<' || raw[len(raw)-1] != '>' {
			return "", errors.New("github release pagination is invalid")
		}
		parsed, err := url.Parse(raw[1 : len(raw)-1])
		wantPath := "/repos/" + repository.Owner + "/" + repository.Name + "/releases"
		if err != nil || parsed.Scheme != "https" || parsed.Host != "api.github.com" ||
			parsed.User != nil || parsed.Path != wantPath || parsed.Fragment != "" || parsed.Port() != "" {
			return "", errors.New("github release pagination is invalid")
		}
		query := parsed.Query()
		if query.Get("per_page") != strconv.Itoa(maxReleaseCount) || query.Get("page") == "" || len(query) != 2 {
			return "", errors.New("github release pagination is invalid")
		}
		page, err := strconv.Atoi(query.Get("page"))
		if err != nil || page < 2 || page > maxReleasePages {
			return "", errors.New("github release pagination is invalid")
		}
		return parsed.String(), nil
	}
	return "", nil
}

func selectLatestStableRelease(releases []githubRelease, repository githubRepository) (stableRelease, error) {
	if !validGitHubRepository(repository) {
		return stableRelease{}, errors.New("invalid github repository")
	}
	var selected stableRelease
	var selectedVersion semanticVersion
	found := false
	for _, candidate := range releases {
		if candidate.Draft || candidate.Prerelease || !candidate.Immutable {
			continue
		}
		version, ok := parseStableSemanticVersion(candidate.TagName)
		if !ok {
			continue
		}
		published, err := time.Parse(time.RFC3339, candidate.PublishedAt)
		if err != nil {
			continue
		}
		if found && version.compare(selectedVersion) <= 0 {
			continue
		}
		name := sanitizeText(candidate.Name, 256, false)
		if name == "" {
			name = candidate.TagName
		}
		selected = stableRelease{
			Tag:         candidate.TagName,
			Version:     version.normalized,
			Name:        name,
			Notes:       sanitizeText(candidate.Body, 32*1024, true),
			URL:         "https://github.com/" + repository.Owner + "/" + repository.Name + "/releases/tag/" + candidate.TagName,
			PublishedAt: published.UTC(),
		}
		selectedVersion = version
		found = true
	}
	if !found {
		return stableRelease{}, errors.New("no stable semantic release found")
	}
	return selected, nil
}

func parseStableSemanticVersion(tag string) (semanticVersion, bool) {
	if len(tag) == 0 || len(tag) > 128 || !utf8.ValidString(tag) || hasControl(tag, false) {
		return semanticVersion{}, false
	}
	matches := stableVersionPattern.FindStringSubmatch(tag)
	if matches == nil {
		return semanticVersion{}, false
	}
	major, errMajor := strconv.ParseUint(matches[1], 10, 64)
	minor, errMinor := strconv.ParseUint(matches[2], 10, 64)
	patch, errPatch := strconv.ParseUint(matches[3], 10, 64)
	if errMajor != nil || errMinor != nil || errPatch != nil {
		return semanticVersion{}, false
	}
	return semanticVersion{
		major:      major,
		minor:      minor,
		patch:      patch,
		normalized: strings.TrimPrefix(tag, "v"),
	}, true
}

func parseNormalizedSemanticVersion(version string) (semanticVersion, bool) {
	parsed, ok := parseStableSemanticVersion("v" + version)
	if !ok || parsed.normalized != version {
		return semanticVersion{}, false
	}
	return parsed, true
}

func parseGitHubRepository(raw string) (githubRepository, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512 || !utf8.ValidString(raw) || hasControl(raw, false) {
		return githubRepository{}, errors.New("invalid github remote")
	}
	var repositoryPath string
	switch {
	case strings.HasPrefix(raw, "git@github.com:"):
		repositoryPath = strings.TrimPrefix(raw, "git@github.com:")
		if strings.ContainsAny(repositoryPath, "?#") {
			return githubRepository{}, errors.New("invalid github remote")
		}
	default:
		parsed, err := url.Parse(raw)
		if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Port() != "" {
			return githubRepository{}, errors.New("invalid github remote")
		}
		switch parsed.Scheme {
		case "https":
			if parsed.User != nil || parsed.Hostname() != "github.com" {
				return githubRepository{}, errors.New("invalid github remote")
			}
		case "ssh":
			if parsed.User == nil || parsed.User.Username() != "git" || parsed.User.String() != "git" || parsed.Hostname() != "github.com" {
				return githubRepository{}, errors.New("invalid github remote")
			}
		default:
			return githubRepository{}, errors.New("invalid github remote")
		}
		if parsed.RawPath != "" || strings.Contains(parsed.EscapedPath(), "%") {
			return githubRepository{}, errors.New("invalid github remote")
		}
		repositoryPath = strings.TrimPrefix(parsed.Path, "/")
	}
	if strings.HasSuffix(repositoryPath, ".git") {
		repositoryPath = strings.TrimSuffix(repositoryPath, ".git")
	}
	parts := strings.Split(repositoryPath, "/")
	if len(parts) != 2 {
		return githubRepository{}, errors.New("invalid github remote")
	}
	repository := githubRepository{Owner: parts[0], Name: parts[1]}
	if !validGitHubRepository(repository) {
		return githubRepository{}, errors.New("invalid github remote")
	}
	return repository, nil
}

func validGitHubRepository(repository githubRepository) bool {
	return githubOwnerPattern.MatchString(repository.Owner) &&
		githubNamePattern.MatchString(repository.Name) &&
		repository.Name != "." && repository.Name != ".."
}

func sanitizeText(value string, maximum int, multiline bool) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(char rune) rune {
		if char < 0x20 && !(multiline && (char == '\r' || char == '\n' || char == '\t')) {
			return -1
		}
		return char
	}, value)
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
