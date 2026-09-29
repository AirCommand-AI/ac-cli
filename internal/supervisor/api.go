package supervisor

import (
	"context"
	"github.com/AirCommand-AI/ac-cli/internal/agentapi"
	"time"
)

// Supervisor owns the lifecycle of daemon-run agents. Only the control API
// changes Desired; all observed fields are written by the supervisor.
type Supervisor interface {
	Start(ctx context.Context, def AgentDefinition) error
	Stop(ctx context.Context, name string) error
	Remove(ctx context.Context, name string) error
	List(ctx context.Context) ([]AgentStatus, error)
	Shutdown(ctx context.Context, stopAgents bool) error
	Run(ctx context.Context) error
}

type AgentDefinition struct {
	Version          int         `json:"version"`
	AgentID          string      `json:"agentId"`
	Name             string      `json:"name"`
	Organization     string      `json:"organization"`
	Workstream       string      `json:"workstream"`
	WorkFolder       string      `json:"workFolder"`
	Repos            []string    `json:"repos"`
	Harness          string      `json:"harness"`
	Mode             string      `json:"mode"`
	Desired          string      `json:"desired"`
	State            string      `json:"state"`
	Crashes          []time.Time `json:"crashes,omitempty"`
	LastExit         *Exit       `json:"lastExit,omitempty"`
	SessionStartedAt string      `json:"sessionStartedAt,omitempty"`
}
type Exit struct {
	At     string `json:"at"`
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
}
type AgentStatus struct {
	Name       string `json:"name"`
	AgentID    string `json:"agentId"`
	Workstream string `json:"workstream"`
	Desired    string `json:"desired"`
	State      string `json:"state"`
	PID        int    `json:"pid,omitempty"`
	LastExit   *Exit  `json:"lastExit,omitempty"`
	LastPollAt string `json:"lastPollAt,omitempty"`
}

// Notification contains pointers only, never the message body.
type Notification = agentapi.Notification
type Feed struct {
	Notifications []Notification
	Cursor        string
	PollAfter     time.Duration
}

// Poller encapsulates the extracted agent API: the caller owns the cursor,
// while the implementation fetches, classifies terminal errors and composes
// the existing spool record (including senderId and summary).
type Poller interface {
	Fetch(context.Context, AgentDefinition, string, bool) (Feed, error)
	Spool(context.Context, AgentDefinition, Notification) (any, error)
}

// ErrAgentStopped is returned by Poller on terminal dashboard Stop/Remove.
var ErrAgentStopped = &terminalError{}

type terminalError struct{}

func (*terminalError) Error() string { return "agent stopped or removed by dashboard" }

// Tmux isolates process control for testing. Implementations must use the
// dedicated aircom tmux socket, never the operator's default server.
type Tmux interface {
	Inspect(context.Context, string) (Pane, error)
	Start(context.Context, AgentDefinition, []string) error
	Kill(context.Context, string) error
}
type Pane struct {
	Exists   bool
	Dead     bool
	ExitCode int
	Signal   string
	PID      int
}
