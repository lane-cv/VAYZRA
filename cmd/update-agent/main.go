package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultListen         = ":8765"
	defaultStateDirectory = "/var/lib/happylearn-update"
	maxOutput             = 16 * 1024
)

var (
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
	stateDirectory  string
	port            string
	internalPort    string
	token           string
	githubToken     string
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "update-agent: configuration failed")
		os.Exit(1)
	}
	a, err := newAgent(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "update-agent: initialization failed")
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", a.health)
	mux.HandleFunc("/v1/status", a.statusHandler)
	mux.HandleFunc("/v1/check", a.checkHandler)
	mux.HandleFunc("/v1/apply", a.applyHandler)
	mux.HandleFunc("/v1/rollback", a.rollbackHandler)
	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      45 * time.Second,
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
		repository:      strings.TrimSpace(os.Getenv("UPDATE_AGENT_REPOSITORY")),
		ref:             valueOr("UPDATE_AGENT_REF", "master"),
		project:         valueOr("UPDATE_AGENT_PROJECT", "happylearn-dev"),
		envFile:         strings.TrimSpace(os.Getenv("UPDATE_AGENT_ENV_FILE")),
		aiEnvFile:       strings.TrimSpace(os.Getenv("UPDATE_AGENT_AI_ENV_FILE")),
		composeOverride: strings.TrimSpace(os.Getenv("UPDATE_AGENT_COMPOSE_OVERRIDE")),
		licenseFile:     strings.TrimSpace(os.Getenv("UPDATE_AGENT_LICENSE_FILE")),
		tokenFile:       strings.TrimSpace(os.Getenv("UPDATE_AGENT_TOKEN_FILE")),
		githubTokenFile: strings.TrimSpace(os.Getenv("UPDATE_AGENT_GITHUB_TOKEN_FILE")),
		stateDirectory:  valueOr("UPDATE_AGENT_STATE_DIRECTORY", defaultStateDirectory),
		port:            valueOr("UPDATE_AGENT_APP_PORT", "8080"),
		internalPort:    valueOr("UPDATE_AGENT_INTERNAL_PORT", "9090"),
	}
	paths := []string{
		cfg.repository, cfg.envFile, cfg.aiEnvFile, cfg.composeOverride,
		cfg.licenseFile, cfg.tokenFile, cfg.githubTokenFile, cfg.stateDirectory,
	}
	for _, path := range paths {
		if path == "" || len(path) > 4096 || !filepath.IsAbs(path) {
			return config{}, errors.New("invalid update agent configuration")
		}
	}
	if !validBranchRef(cfg.ref) || !projectPattern.MatchString(cfg.project) ||
		!validPort(cfg.port) || !validPort(cfg.internalPort) {
		return config{}, errors.New("invalid update agent configuration")
	}
	if !pathWithin(cfg.repository, cfg.composeOverride) {
		return config{}, errors.New("invalid compose override")
	}
	for _, path := range []string{
		cfg.repository, cfg.envFile, cfg.aiEnvFile, cfg.composeOverride,
		cfg.licenseFile, cfg.tokenFile, cfg.githubTokenFile,
	} {
		if _, err := os.Stat(path); err != nil {
			return config{}, errors.New("update agent file is unavailable")
		}
	}
	if err := ensureStateDirectory(cfg.stateDirectory); err != nil {
		return config{}, errors.New("update agent state directory is unavailable")
	}
	token, err := os.ReadFile(cfg.tokenFile)
	if err != nil || len(strings.TrimSpace(string(token))) < 32 || len(token) > 4096 {
		return config{}, errors.New("update agent token is invalid")
	}
	cfg.token = strings.TrimSpace(string(token))
	if hasControl(cfg.token, false) {
		return config{}, errors.New("update agent token is invalid")
	}
	githubToken, err := os.ReadFile(cfg.githubTokenFile)
	if err != nil || len(githubToken) > 4096 {
		return config{}, errors.New("github token file is invalid")
	}
	cfg.githubToken = strings.TrimSpace(string(githubToken))
	if strings.HasPrefix(cfg.githubToken, "development-github-token-") {
		cfg.githubToken = ""
	}
	if hasControl(cfg.githubToken, false) {
		return config{}, errors.New("github token is invalid")
	}
	return cfg, nil
}

func ensureStateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid state directory")
	}
	return os.Chmod(path, 0o700)
}

func validBranchRef(value string) bool {
	if !refPattern.MatchString(value) || strings.Contains(value, "..") ||
		strings.Contains(value, "//") || strings.Contains(value, "@{") ||
		strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") ||
		strings.HasSuffix(value, ".lock") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

func validPort(value string) bool {
	port, err := strconv.Atoi(value)
	return err == nil && port > 0 && port <= 65535 && strconv.Itoa(port) == value
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
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
		code := http.StatusServiceUnavailable
		if errors.Is(err, errBusy) {
			code = http.StatusConflict
		}
		writeJSON(w, code, map[string]string{"error": genericStatusMessage(a.snapshot(), "检查 GitHub Release 失败")})
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
		code := http.StatusServiceUnavailable
		switch {
		case errors.Is(err, errBusy):
			code = http.StatusConflict
		case errors.Is(err, errDirty):
			code = http.StatusPreconditionFailed
		}
		writeJSON(w, code, map[string]string{"error": genericStatusMessage(a.snapshot(), "更新未启动")})
		return
	}
	writeJSON(w, http.StatusAccepted, status)
}

func (a *agent) rollbackHandler(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(r) || r.Method != http.MethodPost || r.URL.RawQuery != "" || !emptyBody(r) {
		authorizedError(w)
		return
	}
	_, err := a.rollback(r.Context())
	if errors.Is(err, errRollbackUnavailable) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "当前更新架构不支持安全的手动回滚；更新失败时会自动恢复旧运行态"})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "回滚服务暂不可用"})
}

func genericStatusMessage(status updateStatus, fallback string) string {
	if status.Message == "" {
		return fallback
	}
	return status.Message
}

func (a *agent) authorized(r *http.Request) bool {
	if r == nil {
		return false
	}
	const prefix = "Bearer "
	raw := r.Header.Get("Authorization")
	return strings.HasPrefix(raw, prefix) && len(raw) == len(prefix)+len(a.cfg.token) &&
		subtle.ConstantTimeCompare([]byte(raw[len(prefix):]), []byte(a.cfg.token)) == 1
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
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func hasControl(value string, multiline bool) bool {
	for _, char := range value {
		if char < 0x20 && !(multiline && (char == '\r' || char == '\n' || char == '\t')) {
			return true
		}
	}
	return false
}
