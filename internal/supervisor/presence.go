package supervisor

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentstate"
	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

// stepPresence is called with m.mu held. Network reporting and RPC sends run
// outside the lock, from the daemon tick; event consumers never block on HTTP.
func (m *Manager) stepPresence(a *managed, e AgentEvent) {
	if m.StateReport == nil {
		return
	}
	if a.presence.State.Since.IsZero() {
		program := a.def.Program
		if program == "" {
			program = "pi"
		}
		a.presence = agentstate.New(program, e.At)
	}
	if e.Reason != "" {
		runes := []rune(strings.ToValidUTF8(e.Reason, ""))
		if len(runes) > 120 {
			e.Reason = string(runes[:120])
		}
	}
	previous := a.presence
	current, nudge := agentstate.Step(previous, agentstate.Event{Kind: e.Kind, Logical: agentstate.Logical(e.Logical), Reason: e.Reason, TaskNumber: e.TaskNumber, At: e.At})
	a.presence = current
	if nudge != agentstate.NoNudge {
		a.pendingPresenceNudges = append(a.pendingPresenceNudges, nudge)
	}
}
func (m *Manager) progressPresence(ctx context.Context, a *managed) {
	if m.StateReport == nil {
		return
	}
	now := m.now()
	if a.presence.State.Since.IsZero() {
		program := a.def.Program
		if program == "" {
			program = "pi"
		}
		a.presence = agentstate.New(program, now)
	}
	alive := a.def.State == "running" && a.def.Desired == "running" && !(m.stoppingHold && a.machineStopped)
	if alive && a.presence.State.Physical != agentstate.Running {
		m.emitAgentEvent(a, AgentEvent{AgentID: a.def.AgentID, Kind: "session_alive", At: now})
	}
	if !alive && a.presence.State.Physical == agentstate.Running {
		kind := "session_exited"
		reason := a.def.Reason
		if m.stoppingHold && a.machineStopped {
			kind = "machine_stopping"
			reason = "machine stopping"
		} else if a.def.State == "stopped-by-dashboard" {
			kind = "dashboard_stop"
		} else if a.def.State == "crashed" {
			kind = "crashed"
		}
		m.emitAgentEvent(a, AgentEvent{AgentID: a.def.AgentID, Kind: kind, At: now, Reason: reason})
	}
	if alive && !a.presence.InRun && a.def.Workstream != "" && !now.Before(a.nextTaskCheck) {
		m.refreshPresenceTasks(ctx, a)
	}
	m.stepPresence(a, AgentEvent{AgentID: a.def.AgentID, Kind: "tick", At: now})
	for _, nudge := range a.pendingPresenceNudges {
		// The pure engine owns the one-per-episode guard; send only to a live
		// process. A reconnect does not replay stale automatic nudges.
		if alive {
			m.sendPresenceNudge(ctx, a, nudge)
		}
	}
	a.pendingPresenceNudges = nil
	// The dashboard stopped or removed this agent and revoked its credential:
	// try the Stopped report once, never retry it (each retry is refused).
	if a.def.State != "stopped-by-dashboard" {
		a.dashboardStopReported = false
	} else if a.dashboardStopReported {
		return
	}
	if a.def.Workstream == "" || now.Before(a.nextPresenceRetry) || !agentstate.Due(a.presenceReported, a.presence.State, a.lastPresenceReport, now) {
		return
	}
	current, def, report := a.presence.State, a.def, m.StateReport
	// Stamp the report when it is sent: an event processed during this pass
	// (e.g. while tasks were fetched) can move Since past the pass start, and
	// the server rejects a report whose Since is after its At.
	at := m.now()
	if at.Before(current.Since) {
		at = current.Since
	}
	m.mu.Unlock()
	reportCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := report(reportCtx, def, current, at)
	cancel()
	m.mu.Lock()
	if def.State == "stopped-by-dashboard" {
		a.dashboardStopReported = true
	}
	if err != nil {
		a.nextPresenceRetry = m.now().Add(30 * time.Second)
		log.Printf("supervisor: presence %s: %v", def.Name, err)
		return
	}
	a.presenceReported = current
	a.lastPresenceReport = m.now()
	a.nextPresenceRetry = time.Time{}
}
func (m *Manager) refreshPresenceTasks(ctx context.Context, a *managed) {
	api, ok := m.Poll.(interface {
		PresenceTasks(context.Context, AgentDefinition) (string, string, string, error)
	})
	if !ok {
		return
	}
	a.nextTaskCheck = m.now().Add(30 * time.Second)
	def := a.def
	m.mu.Unlock()
	approval, blocked, inFlight, err := api.PresenceTasks(ctx, def)
	m.mu.Lock()
	if m.agents[def.Name] != a || a.def.Workstream != def.Workstream {
		return
	}
	if err != nil {
		log.Printf("supervisor: tasks %s: %v", def.Name, err)
		return
	}
	before := a.presence
	if before.Approval != approval {
		kind := "approval_clear"
		if approval != "" {
			kind = "approval_pending"
		}
		m.stepTaskSignal(a, kind, approval)
	}
	if before.Blocked != blocked || before.InFlight != inFlight {
		kind := "task_clear"
		task := ""
		if blocked != "" {
			kind = "task_blocked"
			task = blocked
		} else if inFlight != "" {
			kind = "task_in_flight"
			task = inFlight
		}
		m.stepTaskSignal(a, kind, task)
	}
}
func (m *Manager) stepTaskSignal(a *managed, kind, number string) {
	if a.presence.State.Since.IsZero() {
		return
	}
	m.emitAgentEvent(a, AgentEvent{AgentID: a.def.AgentID, Kind: kind, TaskNumber: number, At: m.now()})
}
func (m *Manager) sendPresenceNudge(ctx context.Context, a *managed, n agentstate.Nudge) {
	text := "You have not made progress in this run for 15 minutes. Continue or report what is blocking you."
	if n == agentstate.NudgeInFlight {
		text = "You have been waiting on an in-flight task for 15 minutes. Continue it or report what is blocking you."
	}
	if a.def.Kind == "attached" {
		m.sendAttachedSignal(a, "nudge", text)
		return
	}
	if a.driver == nil {
		return
	}
	d := a.driver
	m.mu.Unlock()
	err := d.Send(pidriver.Outgoing{Text: text, Kind: pidriver.Urgent, Source: "auto-nudge"})
	m.mu.Lock()
	if err != nil {
		log.Printf("supervisor: nudge %s: %v", a.def.Name, err)
	}
}
