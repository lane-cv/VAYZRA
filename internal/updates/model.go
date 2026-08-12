package updates

import "time"

// Status is the public, administrator-facing state of the local update agent.
// It intentionally contains commit IDs only; command output and credentials
// never cross the application boundary.
type Status struct {
	Enabled         bool       `json:"enabled"`
	State           string     `json:"state"`
	Strategy        string     `json:"strategy"`
	Repository      string     `json:"repository"`
	Ref             string     `json:"ref"`
	Channel         string     `json:"channel"`
	CurrentVersion  string     `json:"currentVersion"`
	LatestVersion   string     `json:"latestVersion"`
	CurrentCommit   string     `json:"currentCommit"`
	LatestCommit    string     `json:"latestCommit"`
	ReleaseName     string     `json:"releaseName"`
	ReleaseNotes    string     `json:"releaseNotes"`
	ReleaseURL      string     `json:"releaseURL"`
	PublishedAt     *time.Time `json:"publishedAt"`
	UpdateAvailable bool       `json:"updateAvailable"`
	Dirty           bool       `json:"dirty"`
	CanRollback     bool       `json:"canRollback"`
	PreviousVersion string     `json:"previousVersion"`
	Phase           string     `json:"phase"`
	Progress        int        `json:"progress"`
	Message         string     `json:"message"`
	StartedAt       *time.Time `json:"startedAt"`
	FinishedAt      *time.Time `json:"finishedAt"`
	LegacyProtocol  bool       `json:"-"`
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

	StrategyGitHubRelease = "github-release"
	ChannelStable         = "stable"

	PhaseIdle       = "idle"
	PhaseChecking   = "checking"
	PhaseFetching   = "fetching"
	PhasePreparing  = "preparing"
	PhaseBuilding   = "building"
	PhaseSwitching  = "switching"
	PhaseVerifying  = "verifying"
	PhaseMerging    = "merging"
	PhaseRecovering = "recovering"
	PhaseComplete   = "complete"
	PhaseFailed     = "failed"
)
