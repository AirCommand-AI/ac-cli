package supervisor

import (
	"context"
	"fmt"
	"os"
	"time"
)

func takeoverGraceActive(since string, now time.Time) bool {
	at, err := time.Parse(time.RFC3339Nano, since)
	return err == nil && now.Sub(at) < 30*time.Second
}

// Called with m.mu held. A slow ps lookup must never hold the supervisor lock.
func (m *Manager) takeoverAliveOutsideLock(a *managed) bool {
	p := a.def.Takeover
	m.mu.Unlock()
	alive := takeoverAlive(p)
	m.mu.Lock()
	if a.def.Takeover != p {
		return true
	} // changed fence: refuse to relaunch
	return alive
}

// SetTakeoverNotice installs the connected CLI's notice writer. nil clears it.
func (m *Manager) SetTakeoverNotice(name string, notice func(string) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a := m.agents[name]; a != nil {
		a.takeoverNotice = notice
	}
}

// Takeover fences the headless process before the CLI may open the same
// session in the foreground. The agent lock remains owned by the daemon.
func (m *Manager) Takeover(ctx context.Context, name string) (TakeoverSpec, error) {
	m.mu.Lock()
	a := m.agents[name]
	if a == nil || a.def.Mode != "headless" || a.def.Desired != "running" || a.driver == nil || a.takenOver {
		m.mu.Unlock()
		return TakeoverSpec{}, fmt.Errorf("headless agent %q is not running", name)
	}
	a.takenOver = true
	a.takeoverConnected = true
	d := a.driver
	def := a.def
	m.mu.Unlock()
	if err := d.Stop(ctx); err != nil {
		m.mu.Lock()
		a.takenOver = false
		a.takeoverConnected = false
		m.mu.Unlock()
		return TakeoverSpec{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.def.Desired != "running" {
		a.takenOver = false
		a.takeoverConnected = false
		return TakeoverSpec{}, fmt.Errorf("agent stopped during takeover")
	}
	if a.driver == d {
		a.driver = nil
		m.stopEvents(a)
		m.closeAgentLog(a)
		a.def.Pi = nil
	}
	a.def.State = "taken-over"
	a.def.TakeoverSince = m.now().Format(time.RFC3339Nano)
	a.nextPoll = m.now()
	if err := m.save(a); err != nil {
		a.takenOver = false
		a.takeoverConnected = false
		a.def.State = "starting"
		a.def.TakeoverSince = ""
		return TakeoverSpec{}, err
	}
	args := []string{"--aircommand-workstream", def.Workstream, "--aircommand-agent", def.AgentID, "--aircommand-cli", m.CLI, "--append-system-prompt", m.briefPath(def.AgentID), "--session-id", def.AgentID}
	return TakeoverSpec{PiPath: m.Pi, WorkDir: def.WorkFolder, SessionID: def.AgentID, Args: args}, nil
}
func (m *Manager) RecordTakeover(name string, pid int) error {
	start := takeoverStartTime(pid)
	if pid <= 0 || start == "" {
		return fmt.Errorf("invalid foreground pi pid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[name]
	if a == nil || !a.takenOver || !a.takeoverConnected {
		return fmt.Errorf("agent is not being taken over")
	}
	a.def.Takeover = &TakeoverProcess{PID: pid, StartTime: start}
	return m.save(a)
}
func (m *Manager) ResumeTakeover(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[name]
	if a == nil {
		return os.ErrNotExist
	}
	if !a.takenOver {
		return nil
	}
	a.takeoverConnected = false
	if m.takeoverAliveOutsideLock(a) {
		return fmt.Errorf("foreground pi is still running")
	}
	a.takeoverNotice = nil
	a.def.Takeover = nil
	a.def.TakeoverSince = ""
	a.takenOver = false
	if a.def.Desired == "running" && a.def.State == "taken-over" {
		a.def.State = "starting"
		a.nextStart = time.Time{}
		return m.save(a)
	}
	return nil
}
