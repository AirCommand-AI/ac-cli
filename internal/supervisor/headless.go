package supervisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/adapters/pi"
	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

const startupPrompt = "Check your unread AirCommand messages with aircom inbox and handle them."

// launchHeadless is called with m.mu held. The process start happens outside
// the manager lock, so slow version checks and pi startup do not block wakes.
func (m *Manager) launchHeadless(ctx context.Context, a *managed) error {
	if err := piadapter.VerifyHeadless(m.Home); err != nil {
		a.def.State = "crashed"
		a.def.Reason = err.Error()
		if saveErr := m.save(a); saveErr != nil {
			return fmt.Errorf("%w (saving crashed state: %v)", err, saveErr)
		}
		return err
	}
	if m.Pi == "pi" {
		if _, err := exec.LookPath("pi"); err != nil {
			return fmt.Errorf("pi is required to launch a daemon-started agent; install pi: %w", err)
		}
	}
	if m.NewDriver == nil {
		return fmt.Errorf("headless pi driver is unavailable")
	}
	logPath := filepath.Join(filepath.Dir(m.definitionPath(a.def.AgentID)), "pi.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open pi log: %w", err)
	}
	d := m.NewDriver(f)
	if d == nil {
		f.Close()
		return fmt.Errorf("headless pi driver is unavailable")
	}
	a.logFile = f
	a.driver = d
	a.startupSent = false
	args := []string{"--aircommand-headless", "--aircommand-workstream", a.def.Workstream, "--aircommand-agent", a.def.AgentID, "--aircommand-cli", m.CLI, "--append-system-prompt", m.briefPath(a.def.AgentID)}
	spec := pidriver.LaunchSpec{PiPath: m.Pi, WorkDir: a.def.WorkFolder, SessionID: a.def.AgentID, Args: args}
	if a.legacySession && !a.def.SessionMigrated {
		spec.ForkFrom = migrationSource(a.def.WorkFolder, a.def.AgentID)
	}
	m.mu.Unlock()
	err = d.Start(spec)
	m.mu.Lock()
	if err != nil {
		if a.driver == d {
			a.driver = nil
			m.closeAgentLog(a)
		}
		return err
	}
	if a.driver != d || a.def.Desired != "running" {
		m.mu.Unlock()
		_ = d.Stop(context.Background())
		m.mu.Lock()
		if a.driver == d {
			a.driver = nil
			m.closeAgentLog(a)
		}
		return fmt.Errorf("agent stopped during pi startup")
	}
	snap := d.State()
	a.def.Pi = &PiProcess{PID: snap.PID, PGID: snap.PGID, StartTime: snap.StartTime, Cmdline: strings.Join(snap.Cmdline, " ")}
	a.def.State = "running"
	if !a.legacySession || fixedSessionExists(a.def.WorkFolder, a.def.AgentID) {
		a.def.SessionMigrated = true
	}
	a.def.SessionStartedAt = m.now().Format(time.RFC3339Nano)
	a.nextStart = time.Time{}
	a.nextPoll = m.now()
	if err := m.save(a); err != nil {
		m.discardLaunch(a, d)
		return err
	}
	a.eventsDone = make(chan struct{})
	go m.consumeEvents(a, d, a.eventsDone)
	// Send queues until Ready; it is the first RPC prompt (R6). Never call
	// driver methods that can write to pi under the supervisor mutex.
	m.mu.Unlock()
	err = d.Send(pidriver.Outgoing{Text: startupPrompt, Kind: pidriver.Regular, Source: "startup"})
	m.mu.Lock()
	if err != nil {
		m.discardLaunch(a, d)
		return err
	}
	a.startupSent = true
	return m.drainPending(ctx, a)
}

func (m *Manager) discardLaunch(a *managed, d pidriver.Driver) {
	m.mu.Unlock()
	_ = d.Stop(context.Background())
	m.mu.Lock()
	if a.driver == d {
		a.driver = nil
		m.stopEvents(a)
		m.closeAgentLog(a)
		a.def.Pi = nil
	}
}

func (m *Manager) closeAgentLog(a *managed) {
	if a.logFile != nil {
		_ = a.logFile.Close()
		a.logFile = nil
	}
}

func (m *Manager) tickHeadless(ctx context.Context, a *managed) error {
	if a.driver == nil {
		if !a.nextStart.IsZero() && m.now().Before(a.nextStart) {
			return nil
		}
		return m.launchHeadless(ctx, a)
	}
	select {
	case exit := <-a.driver.Exited():
		a.driver = nil
		m.stopEvents(a)
		m.closeAgentLog(a)
		a.def.Pi = nil
		now := m.now()
		a.def.LastExit = &Exit{At: now.Format(time.RFC3339Nano), Code: exit.Code, Signal: exit.Signal}
		a.def.Crashes = append(a.def.Crashes, now)
		cutoff := now.Add(-10 * time.Minute)
		kept := a.def.Crashes[:0]
		for _, at := range a.def.Crashes {
			if !at.Before(cutoff) {
				kept = append(kept, at)
			}
		}
		a.def.Crashes = kept
		if len(kept) >= 5 {
			a.def.State = "crashed"
		} else {
			a.def.State = "starting"
			a.nextStart = now.Add(backoff(len(kept)))
		}
		return m.save(a)
	default:
	}
	if !a.def.SessionMigrated && fixedSessionExists(a.def.WorkFolder, a.def.AgentID) {
		a.def.SessionMigrated = true
		if err := m.save(a); err != nil {
			return err
		}
	}
	if err := m.drainPending(ctx, a); err != nil {
		return err
	}
	if m.Poll != nil && !m.now().Before(a.nextPoll) {
		return m.poll(ctx, a)
	}
	return nil
}

func (m *Manager) drainPending(ctx context.Context, a *managed) error {
	if a.driver == nil || !a.startupSent || len(a.pendingWakes) == 0 {
		return nil
	}
	pending := a.pendingWakes
	a.pendingWakes = nil
	for _, n := range pending {
		if err := m.deliver(ctx, a, n); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) stopEvents(a *managed) {
	if a.eventsDone != nil {
		close(a.eventsDone)
		a.eventsDone = nil
	}
	for ch := range a.subscribers {
		delete(a.subscribers, ch)
		close(ch)
	}
}
func (m *Manager) consumeEvents(a *managed, d pidriver.Driver, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case e, open := <-d.Events():
			if !open {
				return
			}
			m.mu.Lock()
			if a.driver != d {
				m.mu.Unlock()
				return
			}
			if e.Kind == "agent_settled" {
				m.lastBusy = m.now()
			}
			kind := "rpc"
			switch e.Kind {
			case "agent_start":
				kind = "run_start"
			case "agent_settled":
				kind = "run_end"
			case "tool_execution_start":
				kind = "tool_start"
			case "tool_execution_end":
				kind = "tool_end"
			case "message_start":
				kind = "turn"
			}
			m.emitAgentEvent(a, AgentEvent{AgentID: a.def.AgentID, Kind: kind, At: m.now()})
			for ch := range a.subscribers {
				select {
				case ch <- e:
				default:
					delete(a.subscribers, ch)
					close(ch)
				}
			}
			m.mu.Unlock()
		}
	}
}
