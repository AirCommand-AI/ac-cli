package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/adapters/pi"
	"github.com/AirCommand-AI/ac-cli/internal/claudedriver"
	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

const startupPrompt = "Check your unread AirCommand messages with aircom inbox and handle them."

// launchHeadless is called with m.mu held. The process start happens outside
// the manager lock, so slow version checks and pi startup do not block wakes.
func (m *Manager) launchHeadless(ctx context.Context, a *managed) error {
	if err := m.verifyHarness(a); err != nil {
		return err
	}
	spec, err := m.launchSpec(a.def)
	if errors.Is(err, ErrClaudeNotConnected) {
		// The token may arrive later (bootstrap retry, Integrations): retry.
		a.def.State, a.def.Reason = "starting", err.Error()
		a.nextStart = m.now().Add(time.Minute)
		return m.save(a)
	}
	if err != nil {
		a.def.State = "crashed"
		a.def.Reason = err.Error()
		if saveErr := m.save(a); saveErr != nil {
			return fmt.Errorf("%w (saving crashed state: %v)", err, saveErr)
		}
		return err
	}
	logName := "pi.log"
	if isClaude(a.def) {
		logName = "claude.log"
	}
	logPath := filepath.Join(filepath.Dir(m.definitionPath(a.def.AgentID)), logName)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open agent log: %w", err)
	}
	d := m.newDriver(a.def, f)
	if d == nil {
		f.Close()
		return fmt.Errorf("headless driver is unavailable")
	}
	a.logFile = f
	a.driver = d
	a.startupSent = false
	if !isClaude(a.def) && a.legacySession && !a.def.SessionMigrated {
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
		if errors.Is(exit.Err, claudedriver.ErrAPIKey) {
			// Never restart an agent that is not on the subscription.
			a.def.State, a.def.Reason = "crashed", claudedriver.ErrAPIKey.Error()
		} else if len(kept) >= 5 {
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
			m.reportClaudeEvent(a, e)
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

// verifyHarness checks the harness can start: the pi add-on and binary, or
// the claude binary. A missing piece marks the agent crashed with the reason.
func (m *Manager) verifyHarness(a *managed) error {
	var err error
	switch {
	case isClaude(a.def):
		err = m.checkHarness(a.def)
	default:
		err = piadapter.VerifyHeadless(m.Home)
		if err == nil && m.Pi == "pi" {
			if _, lookErr := exec.LookPath("pi"); lookErr != nil {
				return fmt.Errorf("pi is required to launch a daemon-started agent; install pi: %w", lookErr)
			}
		}
	}
	if err == nil {
		return nil
	}
	a.def.State = "crashed"
	a.def.Reason = err.Error()
	if saveErr := m.save(a); saveErr != nil {
		return fmt.Errorf("%w (saving crashed state: %v)", err, saveErr)
	}
	return err
}
