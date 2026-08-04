package updates

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type AgentClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewAgentClient(baseURL, token string) (*AgentClient, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, ErrInvalid
	}
	base := strings.TrimRight(parsed.String(), "/")
	if token == "" {
		return nil, ErrInvalid
	}
	return &AgentClient{
		baseURL: base,
		token:   token,
		client:  &http.Client{Timeout: 12 * time.Second},
	}, nil
}

func (c *AgentClient) Status(ctx context.Context) (Status, error) {
	return c.call(ctx, http.MethodGet, "/v1/status")
}

func (c *AgentClient) Check(ctx context.Context) (Status, error) {
	return c.call(ctx, http.MethodPost, "/v1/check")
}

func (c *AgentClient) Apply(ctx context.Context) (Status, error) {
	return c.call(ctx, http.MethodPost, "/v1/apply")
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
		return Status{}, agentError(response.StatusCode)
	}
	var status Status
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16*1024))
	if err := decoder.Decode(&status); err != nil || !validStatus(status) {
		return Status{}, ErrAgentUnavailable
	}
	return status, nil
}

func agentError(status int) error {
	switch status {
	case http.StatusConflict:
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
	if !status.Enabled && status.State != StateDisabled {
		return false
	}
	if status.Enabled {
		switch status.State {
		case StateUnknown, StateChecking, StateCurrent, StateAvailable,
			StateUpdating, StateSuccess, StateFailed, StateBlocked:
		default:
			return false
		}
	}
	if status.CurrentCommit != "" && !commitID(status.CurrentCommit) {
		return false
	}
	if status.LatestCommit != "" && !commitID(status.LatestCommit) {
		return false
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
