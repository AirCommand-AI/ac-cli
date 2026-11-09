// Package agentstate derives agent presence from local session events. It has
// no daemon, network or process dependencies; the supervisor supplies events.
package agentstate

import (
	"fmt"
	"strings"
	"time"
)

const (
	// StuckAfter: a tool with no CPU or I/O, or a turn with no model output
	// and no tool, for this long is stuck (design-team-watch).
	StuckAfter  = 5 * time.Minute
	ReportEvery = 25 * time.Minute
)

type Physical string

const (
	Running Physical = "running"
	Stopped Physical = "stopped"
)

type Logical string

const (
	Working Logical = "working"
	Idle    Logical = "idle"
	Waiting Logical = "waiting"
	Stuck   Logical = "stuck"
	// NoActivity is reported only by daemons before agent-status-v2.
	NoActivity Logical = "no_activity"
	Unknown    Logical = "unknown"
)

// State is the K1 wire state. Logical is empty when stopped. Since changes
// only when the physical, logical or reason changes, never on a periodic report.
type State struct {
	Physical Physical  `json:"physical"`
	Logical  Logical   `json:"logical,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Since    time.Time `json:"since"`
	// Tool is the tool a working agent is running (zero: none).
	Tool Tool `json:"tool,omitempty"`
}

// Tool is a running tool; Command only for shell commands, shortened.
type Tool struct {
	Name    string    `json:"name,omitempty"`
	Command string    `json:"command,omitempty"`
	Since   time.Time `json:"since,omitempty"`
}

// Label is "bash: npm install" or the tool name.
func (t Tool) Label() string {
	if t.Command != "" {
		return t.Name + ": " + t.Command
	}
	return t.Name
}

// Event is an ordered signal. The caller assigns At (server timestamps are
// never used to infer local liveness). Task number may be the displayed #N.
type Event struct {
	Kind       string
	At         time.Time
	Logical    Logical // only for kind=state, for programs without the pi add-on
	Reason     string  // explicit stop or state reason
	TaskNumber string
	Tool       string // tool_start: tool name
	Command    string // tool_start: shortened shell command
}

// Snapshot holds sufficient history for re-evaluating timeouts on a tick.
// A value may be copied and fed to Step; Step never mutates its input.
type Snapshot struct {
	State            State
	Program          string // "pi" or "other"
	InRun            bool
	LastActivity     time.Time
	Approval         string
	Blocked          string
	InFlight         string
	Explicit         Logical
	ExplicitReason   string
	NudgedNoActivity bool
	NudgedInFlight   bool
	Tool             Tool
	ToolProgress     time.Time
	DiskFull         bool
	// ModelHangSent: the model-hang interrupt was sent; reset by tool activity.
	ModelHangSent bool
}

type Nudge string

const (
	NoNudge         Nudge = ""
	NudgeNoActivity Nudge = "no_activity"
	NudgeInFlight   Nudge = "in_flight"
	// NudgeModelHang asks the daemon to interrupt a silent model and resend.
	NudgeModelHang Nudge = "model_hang"
)

func New(program string, now time.Time) Snapshot {
	reason := "pi closed"
	if program != "pi" {
		reason = "no listener"
	}
	return Snapshot{Program: program, State: State{Physical: Stopped, Reason: reason, Since: now.UTC()}}
}

// Step applies one event or an elapsed-time tick. A nudge is emitted once per
// episode; a different episode (run, task, or waiting reason) resets its gate.
func Step(previous Snapshot, event Event) (Snapshot, Nudge) {
	s := previous
	now := event.At.UTC()
	if now.IsZero() {
		return previous, NoNudge
	}
	if !s.State.Since.IsZero() && now.Before(s.State.Since) {
		return previous, NoNudge
	}
	stop := ""
	switch event.Kind {
	case "session_alive":
		s.State.Physical = Running
		s.State.Reason = ""
	case "session_exited":
		stop = "pi closed"
	case "no_listener":
		stop = "no listener"
	case "crashed":
		stop = "crashed"
	case "machine_stopping":
		stop = "machine stopping"
	case "dashboard_stop":
		stop = "stopped from the dashboard"
	case "run_start":
		s.InRun, s.LastActivity, s.NudgedNoActivity, s.Tool = true, now, false, Tool{}
	case "turn", "rpc":
		if s.InRun {
			s.LastActivity, s.NudgedNoActivity = now, false
		}
	case "tool_start":
		if s.InRun {
			s.LastActivity, s.ToolProgress, s.ModelHangSent = now, now, false
			s.Tool = Tool{Name: event.Tool, Command: event.Command, Since: now}
		}
	case "tool_end":
		if s.InRun {
			s.LastActivity, s.Tool, s.ModelHangSent = now, Tool{}, false
		}
	case "tool_progress":
		if s.InRun && s.Tool.Name != "" {
			s.ToolProgress = now
		}
	case "disk_full":
		s.DiskFull = true
	case "disk_ok":
		s.DiskFull = false
	case "run_end":
		s.InRun, s.NudgedNoActivity, s.Tool = false, false, Tool{}
	case "approval_pending":
		s.Approval = event.TaskNumber
	case "approval_clear":
		s.Approval = ""
	case "task_blocked":
		s.Blocked, s.InFlight, s.NudgedInFlight = event.TaskNumber, "", false
	case "task_in_flight":
		if s.InFlight != event.TaskNumber {
			s.NudgedInFlight = false
		}
		s.Blocked, s.InFlight = "", event.TaskNumber
	case "task_clear":
		s.Blocked, s.InFlight, s.NudgedInFlight = "", "", false
	case "state":
		if !validLogical(event.Logical) {
			return previous, NoNudge
		}
		s.Explicit, s.ExplicitReason = event.Logical, event.Reason
	case "tick":
	default:
		return previous, NoNudge
	}
	if stop != "" {
		if event.Reason != "" {
			stop = event.Reason
		}
		s.State.Physical = Stopped
		s.State.Logical = ""
		s.InRun, s.Tool = false, Tool{}
		s.State.Reason = stop
	}
	state := s.State
	state.Tool = Tool{}
	if state.Physical == Running {
		state.Reason = ""
		switch {
		case s.Program != "pi" && s.Explicit != "":
			state.Logical, state.Reason = s.Explicit, s.ExplicitReason
		case s.Program != "pi":
			state.Logical = Unknown
		case s.DiskFull:
			state.Logical, state.Reason = Stuck, "disk full"
		case s.InRun && s.Tool.Name != "" && now.Sub(s.ToolProgress) >= StuckAfter:
			state.Logical, state.Reason = Stuck, fmt.Sprintf("tool idle %dm (%s)", int(StuckAfter/time.Minute), s.Tool.Label())
		case s.InRun && s.Tool.Name == "" && !s.LastActivity.IsZero() && now.Sub(s.LastActivity) >= StuckAfter:
			state.Logical, state.Reason = Stuck, fmt.Sprintf("no model response %dm", int(StuckAfter/time.Minute))
		case s.InRun:
			state.Logical, state.Tool = Working, s.Tool
		case s.Approval != "":
			state.Logical, state.Reason = Waiting, "approval for #"+s.Approval
		case s.Blocked != "":
			state.Logical, state.Reason = Waiting, "#"+s.Blocked+" blocked"
		case s.InFlight != "":
			state.Logical, state.Reason = Waiting, "#"+s.InFlight+" in flight"
		default:
			state.Logical = Idle
		}
	}
	// The no-activity reason is an episode label, not a minute counter: the
	// state must not churn each minute or reset its 15-minute nudge clock.
	if state.Logical == NoActivity && previous.State.Logical == NoActivity {
		state.Reason = previous.State.Reason
	}
	if state.Physical != previous.State.Physical || state.Logical != previous.State.Logical || state.Reason != previous.State.Reason {
		state.Since = now
	} else {
		state.Since = previous.State.Since
	}
	s.State = state
	if state.Logical != Waiting || s.Approval != "" || s.Blocked != "" || s.InFlight == "" {
		s.NudgedInFlight = false
	}
	// Nudges come from the dashboard's team watch; the daemon only acts on a
	// silent model, which a message cannot reach.
	var nudge Nudge
	if state.Physical == Running && state.Logical == Stuck && strings.HasPrefix(state.Reason, "no model response") && !s.ModelHangSent {
		s.ModelHangSent, nudge = true, NudgeModelHang
	}
	return s, nudge
}

// Due reports changes immediately and refreshes unchanged presence before the
// server's 30-minute expiry. A failed report should not update lastReported.
func validLogical(value Logical) bool {
	switch value {
	case Working, Idle, Waiting, Stuck, NoActivity, Unknown:
		return true
	}
	return false
}

func Due(previous, current State, lastReported, now time.Time) bool {
	return previous != current || lastReported.IsZero() || !now.Before(lastReported.Add(ReportEvery))
}
