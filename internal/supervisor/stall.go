package supervisor

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

const stallAfter = 15 * time.Minute
const stallRefresh = 10 * time.Minute

type InFlightTask struct {
	ID       string
	Number   int
	Position int
}

type StallAPI interface {
	InFlight(context.Context, AgentDefinition) (InFlightTask, bool, bool, error) // task, found, pending approval
	StateReason(context.Context, AgentDefinition, string, string) error
}

func stallReason(s pidriver.Snapshot, now time.Time, task InFlightTask, hasTask, pendingApproval bool) (string, bool) {
	if s.LastEvent.IsZero() || now.Sub(s.LastEvent) < stallAfter {
		return "", false
	}
	if s.Streaming && !s.Settled {
		if s.CurrentTool != "" {
			return fmt.Sprintf("tool %.85s silent 15m", s.CurrentTool), false
		}
		return "model call silent 15m", false
	}
	if s.Settled && hasTask && !pendingApproval {
		return fmt.Sprintf("idle 15m on task %s", taskLabel(task)), true
	}
	return "", false
}
func taskLabel(task InFlightTask) string {
	if task.Number > 0 {
		return fmt.Sprintf("#%d", task.Number)
	}
	return task.ID
}

// checkStall is called under m.mu; network work happens outside it. The
// reporter and the per-agent RPC driver never acquire m.mu from within Send.
func (m *Manager) checkStall(ctx context.Context, a *managed) {
	api, ok := m.Poll.(StallAPI)
	if !ok || a.driver == nil || m.now().Before(a.nextStallCheck) {
		return
	}
	a.nextStallCheck = m.now().Add(30 * time.Second)
	d := a.driver
	def := a.def
	m.mu.Unlock()
	snap := d.State()
	var task InFlightTask
	var hasTask, pending bool
	var err error
	if snap.Settled && !snap.LastEvent.IsZero() && m.now().Sub(snap.LastEvent) >= stallAfter {
		task, hasTask, pending, err = api.InFlight(ctx, def)
	}
	m.mu.Lock()
	if a.driver != d || err != nil {
		if err != nil {
			log.Printf("supervisor: agent %s: stall check: %v", def.Name, err)
		}
		return
	}
	now := m.now()
	if hasTask && task.ID != a.stallTaskID {
		if a.stallTaskID != "" {
			a.stallTaskSince = now
		}
		a.stallTaskID = task.ID
	}
	reason, nudge := stallReason(snap, now, task, hasTask, pending)
	if a.stallTaskID == task.ID && !a.stallTaskSince.IsZero() && now.Sub(a.stallTaskSince) < stallAfter {
		reason, nudge = "", false
	}
	if reason == "" {
		if a.stalled {
			a.stalled, a.stallReason = false, ""
			state := "idle"
			if snap.Streaming {
				state = "working"
			}
			m.mu.Unlock()
			err = api.StateReason(ctx, def, state, "")
			m.mu.Lock()
			if err != nil {
				log.Printf("supervisor: agent %s: restore state: %v", def.Name, err)
			}
		}
		return
	}
	changed := !a.stalled || reason != a.stallReason
	a.stalled, a.stallReason = true, reason
	if changed || now.Sub(a.lastStallReport) >= stallRefresh {
		m.mu.Unlock()
		err = api.StateReason(ctx, def, "stalled", reason)
		m.mu.Lock()
		if err == nil {
			a.lastStallReport = now
		} else {
			log.Printf("supervisor: agent %s: report stalled: %v", def.Name, err)
		}
	}
	if !nudge || !hasTask || task.ID == "" || a.def.Nudge != nil && a.def.Nudge.TaskID == task.ID {
		return
	}
	// Persist the guard before sending; a daemon restart cannot nudge the
	// same task twice. A failed request is logged, not silently retried.
	previous := a.def.Nudge
	a.def.Nudge = &NudgeState{TaskID: task.ID, NudgedAt: now.Format(time.RFC3339Nano)}
	if err := m.save(a); err != nil {
		a.def.Nudge = previous
		log.Printf("supervisor: persist nudge: %v", err)
		return
	}
	m.mu.Unlock()
	text := fmt.Sprintf("You appear to have stopped making progress on task %s. Continue it, or report what is blocking you.", taskLabel(task))
	err = d.Send(pidriver.Outgoing{Text: text, Kind: pidriver.Urgent, Source: "auto-nudge"})
	m.mu.Lock()
	if err != nil {
		a.def.Nudge = previous
		if saveErr := m.save(a); saveErr != nil {
			log.Printf("supervisor: agent %s: reset failed nudge: %v", def.Name, saveErr)
		}
		log.Printf("supervisor: agent %s: auto-nudge: %v", def.Name, err)
		return
	}
	if updates, ok := m.Poll.(interface {
		NudgeUpdate(context.Context, AgentDefinition, InFlightTask) error
	}); ok {
		m.mu.Unlock()
		updateErr := updates.NudgeUpdate(ctx, def, task)
		m.mu.Lock()
		if updateErr != nil {
			log.Printf("supervisor: agent %s: nudge update: %v", def.Name, updateErr)
		}
	}
}
