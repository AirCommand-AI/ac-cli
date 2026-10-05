package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentlock"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
	"github.com/AirCommand-AI/ac-cli/internal/peercred"
)

// SessionProcessStart returns the OS start token to fence PID reuse.
func SessionProcessStart(pid int) string { return takeoverStartTime(pid) }
func SessionProcessAlive(pid int, start string) bool {
	return takeoverAlive(&TakeoverProcess{PID: pid, StartTime: start})
}

// AgentEvent is the daemon-local input to the state engine. A notification
// carries no message body, only an activity/lifecycle signal.
type AgentEvent struct {
	AgentID    string
	Kind       string
	Logical    string
	TaskNumber string
	At         time.Time
	Reason     string
}
type AgentEvents interface {
	SubscribeAgentEvents() (<-chan AgentEvent, func())
}

func (m *Manager) SubscribeAgentEvents() (<-chan AgentEvent, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agentEvents == nil {
		m.agentEvents = make(map[chan AgentEvent]struct{})
	}
	ch := make(chan AgentEvent, 64)
	m.agentEvents[ch] = struct{}{}
	return ch, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.agentEvents[ch]; ok {
			delete(m.agentEvents, ch)
			close(ch)
		}
	}
}
func (m *Manager) emitAttached(a *managed, kind, logical, reason string) {
	m.emitAgentEvent(a, AgentEvent{AgentID: a.def.AgentID, Kind: kind, Logical: logical, Reason: reason, At: m.now()})
}
func (m *Manager) emitAgentEvent(a *managed, e AgentEvent) {
	m.stepPresence(a, e)
	for ch := range m.agentEvents {
		select {
		case ch <- e:
		default:
		}
	}
}

// Claim is owned by the socket connection until attached or released. A
// stopped attached record already owns its lock, which is reused on resume.
type Claim struct {
	AgentID    string
	Workstream string
	PeerPID    int
	lock       *agentlock.Lock
	reused     bool
	expires    time.Time
}

var ErrSessionHeld = errors.New("agent session is held")

func (m *Manager) Claim(agentID, workstream string, peerPID int) (*Claim, error) {
	if agentID == "" || peerPID <= 0 {
		return nil, fmt.Errorf("invalid agent claim")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claims == nil {
		m.claims = make(map[string]*Claim)
	}
	if m.claims[agentID] != nil {
		return nil, fmt.Errorf("%w by a pending session", ErrSessionHeld)
	}
	for _, a := range m.agents {
		if a.def.AgentID != agentID {
			continue
		}
		if a.def.Kind != "attached" {
			pid := a.pid
			if a.driver != nil {
				pid = a.driver.State().PID
			}
			return nil, fmt.Errorf("%w by daemon session %d (pi)", ErrSessionHeld, pid)
		}
		if a.def.State == "running" {
			return nil, fmt.Errorf("%w by daemon session %d (%s)", ErrSessionHeld, a.def.SessionPID, a.def.Program)
		}
		prior := a.def
		m.mu.Unlock()
		alive := SessionProcessAlive(prior.SessionPID, prior.SessionStart)
		m.mu.Lock()
		if a.def.SessionPID != prior.SessionPID || a.def.SessionStart != prior.SessionStart || m.claims[agentID] != nil {
			return nil, ErrSessionHeld
		}
		if alive {
			return nil, fmt.Errorf("%w by daemon session %d (%s)", ErrSessionHeld, prior.SessionPID, prior.Program)
		}
		claim := &Claim{AgentID: agentID, Workstream: workstream, PeerPID: peerPID, reused: true, expires: m.now().Add(time.Minute)}
		m.claims[agentID] = claim
		return claim, nil
	}
	lock, err := agentlock.Acquire(m.Home, agentID)
	if err != nil {
		if pid := peercred.LockHolder(agentlock.Path(m.Home, agentID)); pid > 0 {
			return nil, fmt.Errorf("%w by an old aircom listener pid %d", ErrSessionHeld, pid)
		}
		return nil, fmt.Errorf("%w by an old aircom listener: %v", ErrSessionHeld, err)
	}
	claim := &Claim{AgentID: agentID, Workstream: workstream, PeerPID: peerPID, lock: lock, expires: m.now().Add(time.Minute)}
	m.claims[agentID] = claim
	return claim, nil
}
func (m *Manager) ReleaseClaim(claim *Claim) {
	if claim == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claims[claim.AgentID] != claim {
		return
	}
	delete(m.claims, claim.AgentID)
	if !claim.reused {
		_ = claim.lock.Release()
	}
}

type Attachment struct {
	AgentID, Name, Workstream, SessionStart, Program, SessionID string
	SessionPID                                                  int
}

func (m *Manager) Attach(claim *Claim, p Attachment) error {
	if p.AgentID == "" || p.SessionPID <= 0 || p.SessionStart == "" || (p.Program != "pi" && p.Program != "other") {
		return fmt.Errorf("invalid session attachment")
	}
	if start := takeoverStartTime(p.SessionPID); start == "" || start != p.SessionStart {
		return fmt.Errorf("session process identity changed")
	}
	if p.Workstream != "" {
		if _, err := credentials.NewStore(m.Home).FindByAgent(p.Workstream, p.AgentID); err != nil {
			return fmt.Errorf("agent has no credential on this machine: %w", err)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if claim != nil {
		if m.claims[claim.AgentID] != claim || claim.AgentID != p.AgentID || m.now().After(claim.expires) {
			return fmt.Errorf("agent claim expired or belongs to another session")
		}
		if claim.Workstream != "" && claim.Workstream != p.Workstream {
			return fmt.Errorf("claim workstream does not match attachment")
		}
	} else if m.claims[p.AgentID] != nil {
		return ErrSessionHeld
	}
	var old *managed
	for _, a := range m.agents {
		if a.def.AgentID == p.AgentID {
			old = a
			break
		}
	}
	if old != nil && old.def.Kind != "attached" {
		return ErrSessionHeld
	}
	if old != nil && (old.def.SessionPID != p.SessionPID || old.def.SessionStart != p.SessionStart) {
		prior := old.def
		m.mu.Unlock()
		alive := SessionProcessAlive(prior.SessionPID, prior.SessionStart)
		m.mu.Lock()
		if old.def.SessionPID != prior.SessionPID || old.def.SessionStart != prior.SessionStart {
			return ErrSessionHeld
		}
		if alive {
			return fmt.Errorf("%w by daemon session %d (%s)", ErrSessionHeld, prior.SessionPID, prior.Program)
		}
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = p.AgentID
	}
	if old != nil {
		name = old.def.Name
	}
	if !validName(name) {
		return fmt.Errorf("invalid agent name")
	}
	if collision := m.agents[name]; collision != nil && collision != old {
		return ErrSessionHeld
	}
	a := old
	if a == nil {
		a = &managed{}
	}
	previous := a.def
	offset := previous.Offset
	if previous.Workstream != p.Workstream {
		offset = 0
		// A cursor from another workstream cannot replay that stream's wakes.
		if previous.Workstream != "" {
			if info, err := os.Stat(m.SpoolPath(p.AgentID)); err == nil {
				offset = info.Size()
			}
		}
	}
	a.def = AgentDefinition{Version: 1, Kind: "attached", AgentID: p.AgentID, Name: name, Workstream: p.Workstream, Desired: "running", State: "running", Mode: "attached", SessionPID: p.SessionPID, SessionStart: p.SessionStart, Program: p.Program, SessionID: p.SessionID, Offset: offset}
	if p.Program == "other" {
		a.def.State = "stopped"
		a.def.Reason = "no listener"
	}
	if a.lock == nil && claim != nil && !claim.reused {
		a.lock = claim.lock
	}
	if a.lock == nil {
		a.def = previous
		return fmt.Errorf("attach requires daemon claim or existing lock")
	}
	if err := m.save(a); err != nil {
		a.def = previous
		if old == nil {
			a.lock = nil
		}
		return err
	}
	m.agents[name] = a
	for ch := range m.pendingSubs[p.SessionPID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	if claim != nil {
		delete(m.claims, p.AgentID)
	}
	a.nextPoll = m.now()
	if p.Program == "other" {
		m.emitAttached(a, "no_listener", "", "no listener")
	} else {
		m.emitAttached(a, "session_alive", "", "")
	}
	return nil
}
func (m *Manager) SessionConnected(agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.agents {
		if a.def.AgentID == agentID && a.def.Kind == "attached" {
			if a.def.State == "stopped-by-dashboard" {
				return fmt.Errorf("session stopped from dashboard")
			}
			if a.def.Program != "other" {
				if a.def.State != "running" {
					return fmt.Errorf("session is stopped: %s", a.def.Reason)
				}
				return nil
			}
			a.attachedConnected = true
			a.def.State = "running"
			a.def.Reason = ""
			if err := m.save(a); err != nil {
				return err
			}
			m.emitAttached(a, "session_alive", "", "")
			return nil
		}
	}
	return os.ErrNotExist
}
func (m *Manager) Detach(agentID, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.agents {
		if a.def.AgentID != agentID || a.def.Kind != "attached" {
			continue
		}
		a.attachedConnected = false
		a.def.State = "stopped"
		a.def.Reason = reason
		if err := m.save(a); err != nil {
			return err
		}
		for ch := range a.wakes {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		m.emitAttached(a, "session_exited", "", reason)
		return nil
	}
	return os.ErrNotExist
}

// tickAttached checks process identity independently of the polling lifecycle.
// It never launches or kills a person's process and keeps its lock on exit.
func (m *Manager) tickAttached(ctx context.Context, a *managed) error {
	if !m.now().Before(a.nextStart) {
		a.nextStart = m.now().Add(5 * time.Second)
		p := &TakeoverProcess{PID: a.def.SessionPID, StartTime: a.def.SessionStart}
		m.mu.Unlock()
		alive := takeoverAlive(p)
		m.mu.Lock()
		if a.def.Kind != "attached" || m.agents[a.def.Name] != a {
			return nil
		}
		if !alive && a.def.State == "running" {
			a.def.State = "stopped"
			a.def.Reason = "pi closed"
			if a.def.Program == "other" {
				a.def.Reason = "no listener"
			}
			if err := m.save(a); err != nil {
				return err
			}
			m.emitAttached(a, "session_exited", "", a.def.Reason)
		}
	}
	if a.def.Workstream == "" && m.PlaceAttached != nil && !m.now().Before(a.nextPlacement) {
		a.nextPlacement = m.now().Add(5 * time.Second)
		previous := a.def
		m.mu.Unlock()
		org, ws, err := m.PlaceAttached(ctx, previous)
		m.mu.Lock()
		if m.agents[a.def.Name] != a || a.def.SessionPID != previous.SessionPID || a.def.SessionStart != previous.SessionStart || a.def.Workstream != "" {
			return nil
		}
		if err != nil {
			return err
		}
		if org != "" && ws != "" {
			a.def.Organization = org
			a.def.Workstream = ws
			a.nextPoll = m.now()
			if err := m.save(a); err != nil {
				a.def = previous
				return err
			}
			for ch := range a.wakes {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
	}
	if a.def.Workstream != "" && m.Poll != nil && !m.now().Before(a.nextPoll) {
		return m.poll(ctx, a)
	}
	return nil
}

func (m *Manager) SessionEvent(agentID, kind, logical string, at time.Time) error {
	// Client timestamps are observational; local receipt is the liveness clock.
	at = m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.agents {
		if a.def.AgentID == agentID && a.def.Kind == "attached" && a.def.State == "running" {
			e := AgentEvent{AgentID: a.def.AgentID, Kind: kind, Logical: logical, At: at}
			m.emitAgentEvent(a, e)
			return nil
		}
	}
	return os.ErrNotExist
}
func (m *Manager) SessionAck(agentID string, offset int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.agents {
		if a.def.AgentID == agentID && a.def.Kind == "attached" {
			if offset < a.def.Offset {
				return fmt.Errorf("session offset went backwards")
			}
			info, err := os.Stat(listenstore.NewStore(m.Home).SpoolPath(agentID))
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if err != nil && offset != 0 || err == nil && offset > info.Size() {
				return fmt.Errorf("session offset exceeds notification spool")
			}
			if offset == a.def.Offset {
				return nil
			}
			previous := a.def.Offset
			a.def.Offset = offset
			if err := m.save(a); err != nil {
				a.def.Offset = previous
				return err
			}
			return nil
		}
	}
	return os.ErrNotExist
}
func (m *Manager) SessionLookup(sessionID string) (AgentDefinition, bool) {
	if sessionID == "" {
		return AgentDefinition{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.agents {
		if a.def.Kind == "attached" && a.def.SessionID == sessionID {
			return a.def, true
		}
	}
	return AgentDefinition{}, false
}

// AwaitAttachment wakes subscriptions opened by a pi add-on before join.
func (m *Manager) AwaitAttachment(pid int) (<-chan struct{}, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingSubs == nil {
		m.pendingSubs = make(map[int]map[chan struct{}]struct{})
	}
	if m.pendingSubs[pid] == nil {
		m.pendingSubs[pid] = make(map[chan struct{}]struct{})
	}
	ch := make(chan struct{}, 1)
	m.pendingSubs[pid][ch] = struct{}{}
	return ch, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.pendingSubs[pid][ch]; ok {
			delete(m.pendingSubs[pid], ch)
			close(ch)
		}
	}
}
func (m *Manager) SpoolPath(agentID string) string {
	return listenstore.NewStore(m.Home).SpoolPath(agentID)
}
func (m *Manager) SubscribeWakes(agentID string) (<-chan struct{}, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.agents {
		if a.def.AgentID == agentID && a.def.Kind == "attached" {
			if a.wakes == nil {
				a.wakes = make(map[chan struct{}]struct{})
			}
			ch := make(chan struct{}, 1)
			a.wakes[ch] = struct{}{}
			return ch, func() {
				m.mu.Lock()
				defer m.mu.Unlock()
				if _, ok := a.wakes[ch]; ok {
					delete(a.wakes, ch)
					close(ch)
				}
			}, nil
		}
	}
	return nil, nil, os.ErrNotExist
}

type SessionSignal struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (m *Manager) SubscribeSignals(agentID string) (<-chan SessionSignal, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.agents {
		if a.def.AgentID == agentID && a.def.Kind == "attached" {
			if a.signals == nil {
				a.signals = make(map[chan SessionSignal]struct{})
			}
			ch := make(chan SessionSignal, 32)
			a.signals[ch] = struct{}{}
			for _, pending := range a.pendingSignals {
				ch <- pending
			}
			a.pendingSignals = nil
			return ch, func() {
				m.mu.Lock()
				defer m.mu.Unlock()
				if _, ok := a.signals[ch]; ok {
					delete(a.signals, ch)
					close(ch)
				}
			}, nil
		}
	}
	return nil, nil, os.ErrNotExist
}
func (m *Manager) sendAttachedSignal(a *managed, kind, text string) {
	if len(a.signals) == 0 {
		if len(a.pendingSignals) == 32 {
			a.pendingSignals = a.pendingSignals[1:]
		}
		a.pendingSignals = append(a.pendingSignals, SessionSignal{Type: kind, Text: text})
		return
	}
	for ch := range a.signals {
		select {
		case ch <- SessionSignal{Type: kind, Text: text}:
		default:
		}
	}
}
func (m *Manager) Attached(agentID string) (AgentDefinition, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.agents {
		if a.def.Kind == "attached" && a.def.AgentID == agentID {
			return a.def, true
		}
	}
	return AgentDefinition{}, false
}
