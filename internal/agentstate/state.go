// Package agentstate derives agent presence from local session events. It has
// no daemon, network or process dependencies; the supervisor supplies events.
package agentstate

import (
	"fmt"
	"time"
)

const (
	QuietAfter  = 15 * time.Minute
	NudgeAfter  = 15 * time.Minute
	ReportEvery = 25 * time.Minute
)

type Physical string

const (
	Running Physical = "running"
	Stopped Physical = "stopped"
)

type Logical string

const (
	Working    Logical = "working"
	Idle       Logical = "idle"
	Waiting    Logical = "waiting"
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
}

// Event is an ordered signal. The caller assigns At (server timestamps are
// never used to infer local liveness). Task number may be the displayed #N.
type Event struct {
	Kind       string
	At         time.Time
	Logical    Logical // only for kind=state, for programs without the pi add-on
	Reason     string  // explicit stop or state reason
	TaskNumber string
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
}

type Nudge string

const (
	NoNudge         Nudge = ""
	NudgeNoActivity Nudge = "no_activity"
	NudgeInFlight   Nudge = "in_flight"
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
		s.InRun, s.LastActivity, s.NudgedNoActivity = true, now, false
	case "turn", "tool_start", "tool_end", "rpc":
		if s.InRun {
			s.LastActivity, s.NudgedNoActivity = now, false
		}
	case "run_end":
		s.InRun, s.NudgedNoActivity = false, false
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
		s.InRun = false
		s.State.Reason = stop
	}
	state := s.State
	if state.Physical == Running {
		state.Reason = ""
		switch {
		case s.Program != "pi" && s.Explicit != "":
			state.Logical, state.Reason = s.Explicit, s.ExplicitReason
		case s.Program != "pi":
			state.Logical = Unknown
		case s.InRun && !s.LastActivity.IsZero() && now.Sub(s.LastActivity) >= QuietAfter:
			state.Logical, state.Reason = NoActivity, fmt.Sprintf("no activity %dm", int(now.Sub(s.LastActivity)/time.Minute))
		case s.InRun:
			state.Logical = Working
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
	var nudge Nudge
	if state.Physical == Running && state.Logical == NoActivity && !s.NudgedNoActivity && now.Sub(state.Since) >= NudgeAfter {
		s.NudgedNoActivity, nudge = true, NudgeNoActivity
	}
	if state.Physical == Running && state.Logical == Waiting && s.Approval == "" && s.Blocked == "" && s.InFlight != "" && !s.NudgedInFlight && now.Sub(state.Since) >= NudgeAfter {
		s.NudgedInFlight, nudge = true, NudgeInFlight
	}
	return s, nudge
}

// Due reports changes immediately and refreshes unchanged presence before the
// server's 30-minute expiry. A failed report should not update lastReported.
func validLogical(value Logical) bool {
	switch value {
	case Working, Idle, Waiting, NoActivity, Unknown:
		return true
	}
	return false
}

func Due(previous, current State, lastReported, now time.Time) bool {
	return previous != current || lastReported.IsZero() || !now.Before(lastReported.Add(ReportEvery))
}

// MachineAgent is the minimal K5 view required by the idle/shutdown rules.
type MachineAgent struct {
	Kind           string // started or attached
	Physical       Physical
	InRun          bool
	Streaming      bool
	Takeover       bool
	DesiredRunning bool
	MachineStopped bool
}

// Busy includes an attached session in a run, but not an attached idle pi.
func Busy(agents []MachineAgent, tmuxAttached bool) bool {
	if tmuxAttached {
		return true
	}
	for _, a := range agents {
		if a.Kind == "attached" && a.Physical == Running && a.InRun {
			return true
		}
		if a.Kind != "attached" && a.DesiredRunning && (a.Streaming || a.Takeover) {
			return true
		}
	}
	return false
}

// IdleSince yields nil while busy and otherwise preserves the most recent
// observed output/busy timestamp; callers persist the returned time.
func IdleSince(now, lastBusy, lastOutput time.Time, agents []MachineAgent, tmuxAttached bool) *time.Time {
	idle, _ := MachineIdle(now, lastBusy, lastOutput, agents, tmuxAttached)
	return idle
}

// MachineIdle returns the updated marker as well as the report value: a running
// attached pi must advance lastBusy, or it would appear idle on run_end.
func MachineIdle(now, lastBusy, lastOutput time.Time, agents []MachineAgent, tmuxAttached bool) (*time.Time, time.Time) {
	if Busy(agents, tmuxAttached) {
		return nil, now.UTC()
	}
	if lastOutput.After(now) {
		lastOutput = now
	}
	if lastOutput.After(lastBusy) {
		lastBusy = lastOutput
	}
	if lastBusy.IsZero() {
		lastBusy = now
	}
	idle := lastBusy.UTC()
	return &idle, idle
}

// AgentsStopped disregards attached sessions: their processes belong to a
// person, so machine shutdown must neither wait on nor signal them.
func AgentsStopped(holding bool, agents []MachineAgent) bool {
	if !holding {
		return false
	}
	for _, a := range agents {
		if a.Kind != "attached" && !a.MachineStopped {
			return false
		}
	}
	return true
}
