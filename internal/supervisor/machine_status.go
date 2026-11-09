package supervisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
	"syscall"
	"time"
)

// AgentsStopped reports whether every daemon-started agent has stopped for the
// run's finishing hold.
func (m *Manager) AgentsStopped() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.stoppingHold {
		return false
	}
	for _, a := range m.agents {
		if a.def.Kind == "attached" {
			continue
		}
		if a.driver != nil || a.takenOver || a.def.Takeover != nil || !a.machineStopped {
			return false
		}
	}
	return true
}

func (m *Manager) machineHoldPath() string {
	return filepath.Join(storagepath.DaemonDirectory(m.Home), "machine-stopping")
}

// StopForMachine stops processes but preserves desired and parked states. A
// machine-stopping hold, unlike daemon shutdown, is not an agent state change.
func (m *Manager) StopForMachine(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.agents {
		if a.def.Kind == "attached" {
			continue
		} // Never signal a person's program or drop its lock.
		if a.machineStopped {
			continue
		}
		if a.driver != nil {
			d := a.driver
			a.driver = nil
			m.stopEvents(a)
			m.mu.Unlock()
			err := d.Stop(ctx)
			m.mu.Lock()
			m.closeAgentLog(a)
			if err != nil {
				return err
			}
			a.def.Pi = nil
			if err := m.save(a); err != nil {
				return err
			}
		}
		if a.def.Mode == "tmux" && a.def.Desired == "running" {
			m.mu.Unlock()
			err := m.Tmux.Kill(ctx, a.def.Name)
			m.mu.Lock()
			if err != nil {
				return err
			}
		}
		if !a.takenOver && a.def.Takeover == nil {
			a.machineStopped = true
		}
		if !a.takenOver {
			m.release(a)
		}
	}
	return nil
}

// BeginRunFinishing is triggered by the authenticated run API. The hold lasts
// until the machine is destroyed.
func (m *Manager) BeginRunFinishing(ctx context.Context) error {
	m.mu.Lock()
	m.runFinishing = true
	m.mu.Unlock()
	return m.holdAndStop(ctx)
}

// holdAndStop sets the stopping hold before stopping, so Tick cannot relaunch
// between agents, and persists it so a daemon restart keeps it.
func (m *Manager) holdAndStop(ctx context.Context) error {
	type foreground struct {
		name    string
		process *TakeoverProcess
		notice  func(string) error
	}
	m.mu.Lock()
	m.stoppingHold = true
	if err := os.MkdirAll(storagepath.DaemonDirectory(m.Home), 0700); err != nil {
		m.mu.Unlock()
		return err
	}
	if err := os.WriteFile(m.machineHoldPath(), []byte("stopping\n"), 0600); err != nil {
		m.mu.Unlock()
		return err
	}
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
	shutdownErr := m.StopForMachine(ctx) // Stop other agents even if a takeover is unfenced.
	if stopErr != nil {
		return stopErr
	}
	return shutdownErr
}
