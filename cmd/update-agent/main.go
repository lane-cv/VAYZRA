package main

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	defaultListen = ":8765"
	maxOutput     = 16 * 1024
)

var (
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	refPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	projectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

type config struct {
	listen          string
	repository      string
	ref             string
	project         string
	envFile         string
	aiEnvFile       string
	composeOverride string
	licenseFile     string
	tokenFile       string
	githubTokenFile string
	port            string
	internalPort    string
	token           string
	githubToken     string
}

type agent struct {
	cfg    config
	mu     sync.Mutex
	status updateStatus
}

type updateStatus struct {
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

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "update-agent: configuration failed")
		os.Exit(1)
	}
	a := &agent{cfg: cfg, status: updateStatus{
		Enabled: true, State: "unknown", Ref: cfg.ref,
		Message: "尚未检查 GitHub 更新",
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", a.health)
	mux.HandleFunc("/v1/status", a.statusHandler)
	mux.HandleFunc("/v1/check", a.checkHandler)
	mux.HandleFunc("/v1/apply", a.applyHandler)
	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "update-agent: server failed")
		os.Exit(1)
	}
}

func loadConfig() (config, error) {
	cfg := config{
		listen:          valueOr("UPDATE_AGENT_LISTEN", defaultListen),
		repository:      os.Getenv("UPDATE_AGENT_REPOSITORY"),
		ref:             valueOr("UPDATE_AGENT_REF", "master"),
		project:         valueOr("UPDATE_AGENT_PROJECT", "happylearn-dev"),
		envFile:         os.Getenv("UPDATE_AGENT_ENV_FILE"),
		aiEnvFile:       os.Getenv("UPDATE_AGENT_AI_ENV_FILE"),
		composeOverride: os.Getenv("UPDATE_AGENT_COMPOSE_OVERRIDE"),
		licenseFile:     os.Getenv("UPDATE_AGENT_LICENSE_FILE"),
		tokenFile:       os.Getenv("UPDATE_AGENT_TOKEN_FILE"),
		githubTokenFile: os.Getenv("UPDATE_AGENT_GITHUB_TOKEN_FILE"),
		port:            valueOr("UPDATE_AGENT_APP_PORT", "8080"),
		internalPort:    valueOr("UPDATE_AGENT_INTERNAL_PORT", "9090"),
	}
	if !filepath.IsAbs(cfg.repository) || !filepath.IsAbs(cfg.envFile) ||
		!filepath.IsAbs(cfg.aiEnvFile) || !filepath.IsAbs(cfg.composeOverride) || !filepath.IsAbs(cfg.licenseFile) ||
		!filepath.IsAbs(cfg.tokenFile) || !filepath.IsAbs(cfg.githubTokenFile) || cfg.ref == "" || !refPattern.MatchString(cfg.ref) ||
		!projectPattern.MatchString(cfg.project) || cfg.port == "" || cfg.internalPort == "" {
		return config{}, errors.New("invalid update agent configuration")
	}
	for _, path := range []string{cfg.repository, cfg.envFile, cfg.aiEnvFile, cfg.composeOverride, cfg.licenseFile, cfg.tokenFile, cfg.githubTokenFile} {
		if _, err := os.Stat(path); err != nil {
			return config{}, errors.New("update agent file is unavailable")
		}
	}
	token, err := os.ReadFile(cfg.tokenFile)
	if err != nil || len(strings.TrimSpace(string(token))) < 32 {
		return config{}, errors.New("update agent token is invalid")
	}
	cfg.token = strings.TrimSpace(string(token))
	githubToken, err := os.ReadFile(cfg.githubTokenFile)
	if err != nil {
		return config{}, errors.New("github token file is invalid")
	}
	cfg.githubToken = strings.TrimSpace(string(githubToken))
	if cfg.githubToken == "development-github-token-do-not-use-in-production" {
		cfg.githubToken = ""
	}
	if strings.ContainsAny(cfg.githubToken, "\r\n") || len(cfg.githubToken) > 4096 {
		return config{}, errors.New("github token is invalid")
	}
	return cfg, nil
}

func valueOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func (a *agent) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.RawQuery != "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *agent) statusHandler(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(r) || r.Method != http.MethodGet || r.URL.RawQuery != "" {
		authorizedError(w)
		return
	}
	writeJSON(w, http.StatusOK, a.snapshot())
}

func (a *agent) checkHandler(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(r) || r.Method != http.MethodPost || r.URL.RawQuery != "" || !emptyBody(r) {
		authorizedError(w)
		return
	}
	status, err := a.check(r.Context())
	if err != nil {
		message := a.snapshot().Message
		if message == "" {
			message = "检查 GitHub 更新失败"
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": message})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *agent) applyHandler(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(r) || r.Method != http.MethodPost || r.URL.RawQuery != "" || !emptyBody(r) {
		authorizedError(w)
		return
	}
	status, err := a.apply()
	if err != nil {
		statusCode := http.StatusServiceUnavailable
		if errors.Is(err, errBusy) {
			statusCode = http.StatusConflict
		} else if errors.Is(err, errDirty) {
			statusCode = http.StatusPreconditionFailed
		}
		message := a.snapshot().Message
		if message == "" {
			message = "更新未启动"
		}
		writeJSON(w, statusCode, map[string]string{"error": message})
		return
	}
	writeJSON(w, http.StatusAccepted, status)
}

func (a *agent) authorized(r *http.Request) bool {
	if r == nil {
		return false
	}
	const prefix = "Bearer "
	raw := r.Header.Get("Authorization")
	return strings.HasPrefix(raw, prefix) &&
		len(raw) == len(prefix)+len(a.cfg.token) &&
		subtle.ConstantTimeCompare([]byte(raw[len(prefix):]), []byte(a.cfg.token)) == 1
}

func (a *agent) snapshot() updateStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

func (a *agent) check(ctx context.Context) (updateStatus, error) {
	a.mu.Lock()
	if a.status.State == "checking" || a.status.State == "updating" {
		a.mu.Unlock()
		return updateStatus{}, errBusy
	}
	a.status.State = "checking"
	a.status.Message = "正在检查 GitHub 更新"
	a.mu.Unlock()

	status, err := a.inspect(ctx)
	if err != nil {
		a.setFailure("检查 GitHub 更新失败，请确认远程地址可访问且已配置 GitHub Token", false)
		return updateStatus{}, err
	}
	a.mu.Lock()
	a.status = status
	a.mu.Unlock()
	return status, nil
}

func (a *agent) inspect(ctx context.Context) (updateStatus, error) {
	current, err := a.git(ctx, "rev-parse", "HEAD")
	if err != nil || !commitPattern.MatchString(current) {
		return updateStatus{}, errors.New("current commit unavailable")
	}
	remoteURL, err := a.remoteURL(ctx)
	if err != nil {
		return updateStatus{}, err
	}
	remote, err := a.gitNetwork(ctx, "ls-remote", remoteURL, "refs/heads/"+a.cfg.ref)
	if err != nil {
		return updateStatus{}, err
	}
	fields := strings.Fields(remote)
	if len(fields) < 1 || !commitPattern.MatchString(fields[0]) {
		return updateStatus{}, errors.New("remote commit unavailable")
	}
	dirtyOutput, err := a.git(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return updateStatus{}, err
	}
	dirty := strings.TrimSpace(dirtyOutput) != ""
	state := "current"
	message := "当前已是最新版本"
	if current != fields[0] {
		state = "available"
		message = "发现新的 GitHub 提交"
	}
	if dirty {
		state = "blocked"
		message = "部署目录有未提交修改，无法自动更新"
	}
	return updateStatus{
		Enabled: true, State: state, Repository: remoteURL, Ref: a.cfg.ref,
		CurrentCommit: current, LatestCommit: fields[0],
		UpdateAvailable: current != fields[0], Dirty: dirty, Message: message,
	}, nil
}

var (
	errBusy  = errors.New("update busy")
	errDirty = errors.New("update checkout dirty")
)

func (a *agent) apply() (updateStatus, error) {
	a.mu.Lock()
	if a.status.State == "checking" || a.status.State == "updating" {
		a.mu.Unlock()
		return updateStatus{}, errBusy
	}
	if a.status.Dirty {
		a.mu.Unlock()
		return updateStatus{}, errDirty
	}
	now := time.Now().UTC()
	a.status.State = "updating"
	a.status.Message = "正在拉取、构建并重启服务"
	a.status.StartedAt = &now
	a.status.FinishedAt = nil
	a.mu.Unlock()
	go a.runUpdate()
	return a.snapshot(), nil
}

func (a *agent) runUpdate() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	inspected, err := a.inspect(ctx)
	if err != nil {
		a.setFailure("更新前检查失败", false)
		return
	}
	a.mu.Lock()
	a.status.CurrentCommit = inspected.CurrentCommit
	a.status.LatestCommit = inspected.LatestCommit
	a.status.UpdateAvailable = inspected.UpdateAvailable
	a.status.Dirty = inspected.Dirty
	a.mu.Unlock()
	status := a.snapshot()
	if status.Dirty {
		a.setFailure("部署目录有未提交修改，已停止更新", true)
		return
	}
	if !status.UpdateAvailable {
		a.setComplete("当前已是最新版本")
		return
	}
	remoteURL, err := a.remoteURL(ctx)
	if err != nil {
		a.setFailure("读取 GitHub 远程地址失败", true)
		return
	}
	if _, err := a.gitNetwork(ctx, "fetch", "--prune", remoteURL, a.cfg.ref); err != nil {
		a.setFailure("拉取 GitHub 代码失败", true)
		return
	}
	if _, err := a.git(ctx, "merge", "--ff-only", "FETCH_HEAD"); err != nil {
		a.setFailure("合并 GitHub 代码失败", true)
		return
	}
	if err := a.compose(ctx, "build", "app", "worker"); err != nil {
		a.setFailure("构建新版本失败", true)
		return
	}
	if err := a.compose(ctx, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "300", "app", "worker"); err != nil {
		a.setFailure("重启服务失败", true)
		return
	}
	current, err := a.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		a.setFailure("更新完成状态读取失败", true)
		return
	}
	a.mu.Lock()
	now := time.Now().UTC()
	a.status.State = "success"
	a.status.CurrentCommit = current
	a.status.LatestCommit = current
	a.status.UpdateAvailable = false
	a.status.Dirty = false
	a.status.Message = "更新完成，服务已重新启动"
	a.status.FinishedAt = &now
	a.mu.Unlock()
}

func (a *agent) gitCommand(ctx context.Context, args ...string) *exec.Cmd {
	gitArgs := append([]string{"-c", "safe.directory=" + a.cfg.repository, "-C", a.cfg.repository}, args...)
	return exec.CommandContext(ctx, "git", gitArgs...)
}

func (a *agent) git(ctx context.Context, args ...string) (string, error) {
	output, err := a.gitCommand(ctx, args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func (a *agent) remoteURL(ctx context.Context) (string, error) {
	raw, err := a.git(ctx, "config", "--get", "remote.origin.url")
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(raw, "git@github.com:") {
		return "https://github.com/" + strings.TrimPrefix(raw, "git@github.com:"), nil
	}
	if strings.HasPrefix(raw, "ssh://git@github.com/") {
		return "https://github.com/" + strings.TrimPrefix(raw, "ssh://git@github.com/"), nil
	}
	if strings.HasPrefix(raw, "https://github.com/") {
		return raw, nil
	}
	return "", errors.New("unsupported github remote")
}

func (a *agent) gitNetworkCommand(ctx context.Context, args ...string) *exec.Cmd {
	command := a.gitCommand(ctx, args...)
	command.Env = gitNetworkEnvironment(os.Environ(), a.cfg.githubToken)
	return command
}

func (a *agent) gitNetwork(ctx context.Context, args ...string) (string, error) {
	output, err := a.gitNetworkCommand(ctx, args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func githubBasicAuthorization(token string) string {
	credential := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return "Authorization: Basic " + credential
}

func gitNetworkEnvironment(base []string, token string) []string {
	result := make([]string, 0, len(base)+16)
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		preserveGitTLS := name == "GIT_SSL_CAINFO" || name == "GIT_SSL_CAPATH"
		if !ok || strings.HasPrefix(name, "GIT_") && !preserveGitTLS || name == "GCM_INTERACTIVE" || strings.HasPrefix(name, "SSH_ASKPASS") {
			continue
		}
		result = append(result, entry)
	}

	result = append(result,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false",
		"GCM_INTERACTIVE=never",
	)
	config := [][2]string{
		{"credential.helper", ""},
		{"credential.interactive", "false"},
		{"http.extraHeader", ""},
		{"http.https://github.com/.extraHeader", ""},
	}
	if token != "" {
		config = append(config, [2]string{"http.https://github.com/.extraHeader", githubBasicAuthorization(token)})
	}
	result = append(result, "GIT_CONFIG_COUNT="+fmt.Sprint(len(config)))
	for index, item := range config {
		result = append(result,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", index, item[0]),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", index, item[1]),
		)
	}
	return result
}

func (a *agent) compose(ctx context.Context, args ...string) error {
	composeArgs := []string{
		"compose", "--project-name", a.cfg.project,
		"--project-directory", a.cfg.repository,
		"--env-file", a.cfg.envFile,
		"--env-file", a.cfg.aiEnvFile,
		"-f", filepath.Join(a.cfg.repository, "deploy/compose.dev.yml"),
		"-f", a.cfg.composeOverride,
	}
	composeArgs = append(composeArgs, args...)
	command := exec.CommandContext(ctx, "docker", composeArgs...)
	command.Env = overrideEnvironment(os.Environ(), map[string]string{
		"HAPPYLEARN_UPDATE_REPOSITORY":       a.cfg.repository,
		"HAPPYLEARN_UPDATE_REF":              a.cfg.ref,
		"HAPPYLEARN_UPDATE_PROJECT":          a.cfg.project,
		"HAPPYLEARN_AISTOR_LICENSE_FILE":     a.cfg.licenseFile,
		"HAPPYLEARN_UPDATE_AGENT_TOKEN_FILE": a.cfg.tokenFile,
	})
	command.Dir = a.cfg.repository
	return command.Run()
}

func overrideEnvironment(base []string, values map[string]string) []string {
	result := make([]string, 0, len(base)+len(values))
	seen := make(map[string]bool, len(values))
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if value, replace := values[name]; replace {
			if !seen[name] {
				result = append(result, name+"="+value)
				seen[name] = true
			}
			continue
		}
		if ok {
			result = append(result, entry)
		}
	}
	for name, value := range values {
		if !seen[name] {
			result = append(result, name+"="+value)
		}
	}
	return result
}

func (a *agent) setFailure(message string, updateAvailable bool) {
	a.mu.Lock()
	now := time.Now().UTC()
	a.status.State = "failed"
	a.status.Message = message
	a.status.FinishedAt = &now
	a.status.UpdateAvailable = updateAvailable
	a.mu.Unlock()
}

func (a *agent) setComplete(message string) {
	a.mu.Lock()
	now := time.Now().UTC()
	a.status.State = "success"
	a.status.Message = message
	a.status.FinishedAt = &now
	a.status.UpdateAvailable = false
	a.mu.Unlock()
}

func emptyBody(r *http.Request) bool {
	if r.ContentLength > 0 {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 2))
	return err == nil && len(data) == 0
}

func authorizedError(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
