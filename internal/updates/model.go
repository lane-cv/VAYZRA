package updates

import "time"

// Status is the public, administrator-facing state of the local update agent.
// It intentionally contains commit IDs only; command output and credentials
// never cross the application boundary.
type Status struct {
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

const (
	StateDisabled  = "disabled"
	StateUnknown   = "unknown"
	StateChecking  = "checking"
	StateCurrent   = "current"
	StateAvailable = "available"
	StateUpdating  = "updating"
	StateSuccess   = "success"
	StateFailed    = "failed"
	StateBlocked   = "blocked"
)
