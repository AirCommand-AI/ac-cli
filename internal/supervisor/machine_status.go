package supervisor

import (
	"context"
	"fmt"
	"time"
)

// Activity reports whether a person is attached anywhere on the dedicated
// tmux socket and the newest pane output across the named agent sessions.
type tmuxActivity interface {
	Activity(context.Context, []string) (bool, time.Time, error)
}

// IdleSince returns nil while a model is streaming, a takeover is active, or
// a tmux client is attached. Otherwise it records the latest local activity.
func (m *Manager) IdleSince(ctx context.Context) (*time.Time, error) {
	m.mu.Lock()
	now := m.now()
	busy := false
	names := []string{}
	for _, a := range m.agents {
		if a.def.Desired != "running" {
			continue
		}
		if a.takenOver || a.driver != nil && a.driver.State().Streaming {
			busy = true
		}
		if a.def.Mode == "tmux" {
			names = append(names, a.def.Name)
		}
	}
	m.mu.Unlock()
	var output time.Time
	if len(names) > 0 {
		monitor, ok := m.Tmux.(tmuxActivity)
		if !ok {
			return nil, fmt.Errorf("tmux activity inspection is unavailable")
		}
		attached, last, err := monitor.Activity(ctx, names)
		if err != nil {
			return nil, err
		}
		busy = busy || attached
		output = last
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if busy {
		m.lastBusy = now
		return nil, nil
	}
	if output.After(now) {
		output = now
	}
	if output.After(m.lastBusy) {
		m.lastBusy = output
	}
	if m.lastBusy.IsZero() {
		m.lastBusy = now
	}
	idle := m.lastBusy.UTC()
	return &idle, nil
}

func (m *Manager) AgentsStopped() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.stoppingHold {
		return false
	}
	for _, a := range m.agents {
		if a.driver != nil || a.takenOver || a.def.Takeover != nil || a.def.State != "stopped" {
			return false
		}
	}
	return true
}

// SetMachineState is driven only by an authenticated HTTPS status response.
// The hold is set before stopping so Tick cannot relaunch between agents.
func (m *Manager) SetMachineState(ctx context.Context, state string) error {
	switch state {
	case "stopping":
		m.mu.Lock()
		m.stoppingHold = true
		m.mu.Unlock()
		return m.Shutdown(ctx, true) // keeps every agent's desired=running
	case "online", "error":
		m.mu.Lock()
		m.stoppingHold = false
		m.mu.Unlock()
	}
	return nil
}
