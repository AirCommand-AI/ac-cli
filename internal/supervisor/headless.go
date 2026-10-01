package supervisor

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

const startupPrompt = "Check your unread AirCommand messages with aircom inbox and handle them."

// launchHeadless is called with m.mu held. The process start happens outside
// the manager lock, so slow version checks and pi startup do not block wakes.
func (m *Manager) launchHeadless(a *managed) error {
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
	if !a.def.SessionMigrated {
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
		return fmt.Errorf("agent stopped during pi startup")
	}
	snap := d.State()
	a.def.Pi = &PiProcess{PID: snap.PID, PGID: snap.PGID, StartTime: snap.StartTime, Cmdline: processCmdline(snap.PID)}
	a.def.State = "running"
	a.def.SessionMigrated = true
	a.def.SessionStartedAt = m.now().Format(time.RFC3339Nano)
	a.nextStart = time.Time{}
	a.nextPoll = m.now()
	if err := m.save(a); err != nil {
		return err
	}
	// Send queues until Ready; it is the first RPC prompt (R6). Never call
	// driver methods that can write to pi under the supervisor mutex.
	m.mu.Unlock()
	err = d.Send(pidriver.Outgoing{Text: startupPrompt, Kind: pidriver.Regular, Source: "startup"})
	m.mu.Lock()
	if err != nil {
		return err
	}
	a.startupSent = true
	pending := a.pendingWakes
	a.pendingWakes = nil
	for _, n := range pending {
		if err := m.deliver(a, n); err != nil {
			return err
		}
	}
	return nil
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
		return m.launchHeadless(a)
	}
	select {
	case exit := <-a.driver.Exited():
		a.driver = nil
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
	// Interpreter wrappers may exec after Start. Refresh the durable kernel
	// argv once RPC is ready, before any subsequent daemon restart.
	if snap := a.driver.State(); snap.Ready && a.def.Pi != nil && snap.PID == a.def.Pi.PID && snap.StartTime == a.def.Pi.StartTime {
		if cmdline := processCmdline(snap.PID); cmdline != "" && cmdline != a.def.Pi.Cmdline {
			a.def.Pi.Cmdline = cmdline
			if err := m.save(a); err != nil {
				return err
			}
		}
	}
	for {
		select {
		case e, ok := <-a.driver.Events():
			if !ok {
				return nil
			}
			if e.Kind == "agent_start" || e.Kind == "agent_settled" {
				state := "working"
				if e.Kind == "agent_settled" {
					state = "idle"
				}
				m.reportHeadlessState(ctx, a, state)
			}
		default:
			if m.Poll != nil && !m.now().Before(a.nextPoll) {
				return m.poll(ctx, a)
			}
			return nil
		}
	}
}

func (m *Manager) reportHeadlessState(ctx context.Context, a *managed, state string) {
	reporter, ok := m.Poll.(interface {
		State(context.Context, AgentDefinition, string) error
	})
	if !ok {
		return
	}
	if err := reporter.State(ctx, a.def, state); err != nil {
		// Reporting failure cannot interrupt pi or stop processing wakes.
		log.Printf("supervisor: agent %s: report %s: %v", a.def.Name, state, err)
	}
}
