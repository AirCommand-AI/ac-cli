package supervisor

import (
	"context"
	"fmt"
	"syscall"
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
		type foreground struct {
			name    string
			process *TakeoverProcess
			notice  func(string) error
		}
		m.mu.Lock()
		m.stoppingHold = true
		var active []foreground
		for name, a := range m.agents {
			if a.takenOver || a.def.Takeover != nil {
				active = append(active, foreground{name, a.def.Takeover, a.takeoverNotice})
			}
		}
		m.mu.Unlock()
		var stopErr error
		for _, item := range active {
			if item.process == nil {
				stopErr = fmt.Errorf("takeover %s has no recorded PID; cannot safely stop", item.name)
				continue
			}
			if takeoverAlive(item.process) {
				group, err := syscall.Getpgid(item.process.PID)
				if err != nil || group != item.process.PID || !takeoverAlive(item.process) {
					stopErr = fmt.Errorf("takeover %s has no safe process group", item.name)
					continue
				}
				if item.notice != nil {
					_ = item.notice("machine is stopping; your pi was closed")
				}
			}
			if err := terminateTakeoverGroup(ctx, item.process, 20*time.Second); err != nil {
				stopErr = fmt.Errorf("stop takeover %s: %w", item.name, err)
				continue
			}
			if err := m.ResumeTakeover(item.name); err != nil {
				stopErr = err
			}
		}
		shutdownErr := m.Shutdown(ctx, true) // Stop other agents even if a takeover is unfenced.
		if stopErr != nil {
			return stopErr
		}
		return shutdownErr
	case "online", "error":
		m.mu.Lock()
		m.stoppingHold = false
		m.mu.Unlock()
	}
	return nil
}
