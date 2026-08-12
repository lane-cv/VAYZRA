package updates

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

type AgentClient struct {
	baseURL string
	token   string
	client  *http.Client
}

type legacyAgentStatus struct {
	Enabled         bool       `json:"enabled"`
	State           string     `json:"state"`
	Repository      string     `json:"repository"`
	Ref             string     `json:"ref"`
	CurrentCommit   string     `json:"currentCommit"`
	LatestCommit    string     `json:"latestCommit"`
	UpdateAvailable bool       `json:"updateAvailable"`
	Dirty           bool       `json:"dirty"`
	Message         string     `json:"message"`
	StartedAt       *time.Time `json:"startedAt"`
	FinishedAt      *time.Time `json:"finishedAt"`
}

const maxAgentStatusResponseBytes = 64 * 1024

var legacyStatusFields = map[string]struct{}{
	"enabled": {}, "state": {}, "repository": {}, "ref": {},
	"currentCommit": {}, "latestCommit": {}, "updateAvailable": {},
	"dirty": {}, "message": {}, "startedAt": {}, "finishedAt": {},
}

var currentStatusFields = map[string]struct{}{
	"enabled": {}, "state": {}, "strategy": {}, "repository": {}, "ref": {}, "channel": {},
	"currentVersion": {}, "latestVersion": {}, "currentCommit": {}, "latestCommit": {},
	"releaseName": {}, "releaseNotes": {}, "releaseURL": {}, "publishedAt": {},
	"updateAvailable": {}, "dirty": {}, "canRollback": {}, "previousVersion": {},
	"phase": {}, "progress": {}, "message": {}, "startedAt": {}, "finishedAt": {},
}

func NewAgentClient(baseURL, token string) (*AgentClient, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return nil, ErrInvalid
	}
	base := strings.TrimRight(parsed.String(), "/")
	if token == "" || len(token) > 4096 || !boundedStatusText(token, 4096, false) {
		return nil, ErrInvalid
	}
	return &AgentClient{
		baseURL: base,
		token:   token,
		client:  &http.Client{Timeout: 45 * time.Second},
	}, nil
}

func (c *AgentClient) Status(ctx context.Context) (Status, error) {
	return c.call(ctx, http.MethodGet, "/v1/status")
}

func (c *AgentClient) Check(ctx context.Context) (Status, error) {
	return c.call(ctx, http.MethodPost, "/v1/check")
}

func (c *AgentClient) Apply(ctx context.Context) (Status, error) {
	status, err := c.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	if status.LegacyProtocol {
		return Status{}, ErrAgentProtocolOutdated
	}
	return c.call(ctx, http.MethodPost, "/v1/apply")
}

func (c *AgentClient) Rollback(ctx context.Context) (Status, error) {
	return c.call(ctx, http.MethodPost, "/v1/rollback")
}

func (c *AgentClient) call(ctx context.Context, method, path string) (Status, error) {
	if c == nil || c.client == nil || ctx == nil {
		return Status{}, ErrInvalid
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return Status{}, ErrInvalid
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.client.Do(req)
	if err != nil {
		return Status{}, ErrAgentUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Status{}, agentError(path, response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxAgentStatusResponseBytes+1))
	if err != nil || len(payload) > maxAgentStatusResponseBytes {
		return Status{}, ErrAgentUnavailable
	}
	if status, ok := decodeCurrentStatus(payload); ok {
		return status, nil
	}
	if status, ok := decodeLegacyStatus(payload); ok {
		return status, nil
	}
	return Status{}, ErrAgentUnavailable
}

func decodeCurrentStatus(payload []byte) (Status, bool) {
	var status Status
	if !hasExactJSONFields(payload, currentStatusFields) ||
		!decodeExactJSON(payload, &status) || !validStatus(status) {
		return Status{}, false
	}
	return status, true
}

func decodeLegacyStatus(payload []byte) (Status, bool) {
	var legacy legacyAgentStatus
	if !hasExactJSONFields(payload, legacyStatusFields) ||
		!decodeExactJSON(payload, &legacy) || !validLegacyStatus(legacy) {
		return Status{}, false
	}
	return Status{
		Enabled: true, State: StateBlocked, Strategy: StrategyGitHubRelease,
		Repository: legacy.Repository, Ref: legacy.Ref, Channel: ChannelStable,
		CurrentCommit: legacy.CurrentCommit, LatestCommit: legacy.LatestCommit,
		Dirty: legacy.Dirty, Phase: PhaseComplete, Progress: 100,
		Message:   "更新代理协议过旧，请在宿主机完整重新部署",
		StartedAt: legacy.StartedAt, FinishedAt: legacy.FinishedAt,
		LegacyProtocol: true,
	}, true
}

func hasExactJSONFields(payload []byte, expected map[string]struct{}) bool {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return false
	}
	seen := make(map[string]struct{}, len(expected))
	for decoder.More() {
		token, err := decoder.Token()
		field, ok := token.(string)
		if err != nil || !ok {
			return false
		}
		if _, ok := expected[field]; !ok {
			return false
		}
		if _, duplicate := seen[field]; duplicate {
			return false
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return false
		}
		seen[field] = struct{}{}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != len(expected) {
		return false
	}
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func decodeExactJSON(payload []byte, destination any) bool {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return false
	}
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func validLegacyStatus(status legacyAgentStatus) bool {
	if !status.Enabled || status.Repository != "" && !canonicalGitHubRepositoryURL(status.Repository) ||
		!boundedStatusText(status.Ref, 128, false) || status.Ref == "" ||
		!boundedStatusText(status.Message, 512, false) {
		return false
	}
	switch status.State {
	case StateUnknown, StateChecking, StateCurrent, StateAvailable,
		StateUpdating, StateSuccess, StateFailed, StateBlocked:
	default:
		return false
	}
	if status.CurrentCommit != "" && !commitID(status.CurrentCommit) {
		return false
	}
	if status.LatestCommit != "" && !commitID(status.LatestCommit) {
		return false
	}
	if status.FinishedAt != nil && status.StartedAt != nil && status.FinishedAt.Before(*status.StartedAt) {
		return false
	}
	return true
}

func agentError(path string, status int) error {
	switch status {
	case http.StatusConflict:
		if path == "/v1/rollback" {
			return ErrRollbackUnavailable
		}
		return ErrUpdateBusy
	case http.StatusPreconditionFailed:
		return ErrDirtyCheckout
	case http.StatusNotFound, http.StatusServiceUnavailable:
		return ErrAgentUnavailable
	default:
		return fmt.Errorf("update agent returned HTTP %d", status)
	}
}

func validStatus(status Status) bool {
	if !status.Enabled {
		return status.State == StateDisabled && status.Strategy == "" &&
			status.Repository == "" && status.Ref == "" && status.Channel == "" &&
			status.CurrentVersion == "" && status.LatestVersion == "" &&
			status.CurrentCommit == "" && status.LatestCommit == "" &&
			status.ReleaseName == "" && status.ReleaseNotes == "" && status.ReleaseURL == "" &&
			status.PublishedAt == nil && !status.UpdateAvailable && !status.Dirty &&
			!status.CanRollback && status.PreviousVersion == "" &&
			status.Phase == PhaseIdle && status.Progress == 0 &&
			status.StartedAt == nil && status.FinishedAt == nil &&
			boundedStatusText(status.Message, 512, false)
	}
	if status.Strategy != StrategyGitHubRelease || status.Channel != ChannelStable || status.CanRollback {
		return false
	}
	switch status.State {
	case StateUnknown, StateChecking, StateCurrent, StateAvailable,
		StateUpdating, StateSuccess, StateFailed, StateBlocked:
	default:
		return false
	}
	switch status.Phase {
	case PhaseIdle, PhaseChecking, PhaseFetching, PhasePreparing, PhaseBuilding,
		PhaseSwitching, PhaseVerifying, PhaseMerging, PhaseRecovering,
		PhaseComplete, PhaseFailed:
	default:
		return false
	}
	if status.Progress < 0 || status.Progress > 100 {
		return false
	}
	if !validStateProgress(status) {
		return false
	}
	if status.CurrentCommit != "" && !commitID(status.CurrentCommit) {
		return false
	}
	if status.LatestCommit != "" && !commitID(status.LatestCommit) {
		return false
	}
	if !boundedStatusText(status.Repository, 512, false) ||
		!boundedStatusText(status.Ref, 128, false) ||
		!boundedStatusText(status.CurrentVersion, 128, false) ||
		!boundedStatusText(status.LatestVersion, 128, false) ||
		!boundedStatusText(status.ReleaseName, 256, false) ||
		!boundedStatusText(status.ReleaseNotes, 32*1024, true) ||
		!boundedStatusText(status.ReleaseURL, 2048, false) ||
		!boundedStatusText(status.PreviousVersion, 128, false) ||
		!boundedStatusText(status.Message, 512, false) {
		return false
	}
	if status.Repository != "" && !canonicalGitHubRepositoryURL(status.Repository) {
		return false
	}
	if status.ReleaseURL != "" && !canonicalGitHubReleaseURL(status.ReleaseURL, status.Repository) {
		return false
	}
	if status.PublishedAt != nil && status.ReleaseURL == "" {
		return false
	}
	if status.ReleaseURL != "" && status.PublishedAt == nil {
		return false
	}
	for _, version := range []string{status.CurrentVersion, status.LatestVersion, status.PreviousVersion} {
		if version != "" && (!stableReleaseTag(version) || strings.HasPrefix(version, "v")) {
			return false
		}
	}
	if status.ReleaseURL != "" {
		tag := parsedReleaseTag(status.ReleaseURL, status.Repository)
		if strings.TrimPrefix(tag, "v") != status.LatestVersion {
			return false
		}
	}
	switch status.State {
	case StateAvailable:
		if !status.UpdateAvailable {
			return false
		}
	case StateCurrent, StateSuccess, StateBlocked, StateUnknown:
		if status.UpdateAvailable {
			return false
		}
	}
	if status.FinishedAt != nil && status.StartedAt != nil && status.FinishedAt.Before(*status.StartedAt) {
		return false
	}
	return true
}

func validStateProgress(status Status) bool {
	switch status.State {
	case StateChecking:
		return status.Phase == PhaseChecking && status.Progress >= 0 && status.Progress < 100 && status.FinishedAt == nil
	case StateUpdating:
		return status.Phase != PhaseIdle && status.Phase != PhaseComplete && status.Phase != PhaseFailed &&
			status.Progress >= 0 && status.Progress < 100 && status.StartedAt != nil && status.FinishedAt == nil
	case StateCurrent, StateAvailable, StateBlocked, StateSuccess:
		return status.Phase == PhaseComplete && status.Progress == 100
	case StateFailed:
		return status.Phase == PhaseFailed && status.FinishedAt != nil
	case StateUnknown:
		return status.Phase == PhaseIdle && status.Progress == 0
	default:
		return false
	}
}

func canonicalGitHubRepositoryURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	return len(parts) == 2 && validGitHubOwner(parts[0]) &&
		strings.HasSuffix(parts[1], ".git") && validGitHubRepositoryName(strings.TrimSuffix(parts[1], ".git"))
}

func canonicalGitHubReleaseURL(raw, repository string) bool {
	return parsedReleaseTag(raw, repository) != ""
}

func parsedReleaseTag(raw, repository string) string {
	if repository == "" || !canonicalGitHubRepositoryURL(repository) {
		return ""
	}
	repositoryURL, _ := url.Parse(repository)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return ""
	}
	repositoryPath := strings.TrimSuffix(repositoryURL.Path, ".git")
	prefix := repositoryPath + "/releases/tag/"
	if !strings.HasPrefix(parsed.Path, prefix) {
		return ""
	}
	tag := strings.TrimPrefix(parsed.Path, prefix)
	if !stableReleaseTag(tag) {
		return ""
	}
	return tag
}

func stableReleaseTag(value string) bool {
	if strings.HasPrefix(value, "v") {
		value = strings.TrimPrefix(value, "v")
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 20 || len(part) > 1 && part[0] == '0' {
			return false
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return false
			}
		}
	}
	return true
}

func validGitHubOwner(value string) bool {
	if value == "" || len(value) > 39 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-') {
			return false
		}
	}
	return true
}

func validGitHubRepositoryName(value string) bool {
	if value == "" || len(value) > 100 || value == "." || value == ".." {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.') {
			return false
		}
	}
	return true
}

func boundedStatusText(value string, limit int, multiline bool) bool {
	if len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if char < 0x20 && !(multiline && (char == '\n' || char == '\r' || char == '\t')) {
			return false
		}
	}
	return true
}

func commitID(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
