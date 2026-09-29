package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentlock"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validName(n string) bool { return namePattern.MatchString(n) }

// Manager is serialized across control calls and ticks. The daemon process
// holds agent locks for the entire time it owns an agent, including backoff.
type Manager struct {
	Home, Pi, CLI string
	Tmux          Tmux
	Poll          Poller
	Now           func() time.Time
	mu            sync.Mutex
	agents        map[string]*managed
	booted        chan struct{}
}
type managed struct {
	def                 AgentDefinition
	lock                *agentlock.Lock
	nextStart, nextPoll time.Time
	failures            int
	lastPoll            string
	pid                 int
	delivered           []string
}

func New(home, pi, cli string, tmux Tmux, poll Poller) *Manager {
	return &Manager{Home: home, Pi: pi, CLI: cli, Tmux: tmux, Poll: poll, Now: time.Now, agents: make(map[string]*managed), booted: make(chan struct{})}
}
func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}
func (m *Manager) definitionPath(id string) string {
	return filepath.Join(storagepath.AgentDirectory(m.Home, id), "daemon.json")
}
func (m *Manager) deliveredPath(id string) string {
	return filepath.Join(storagepath.AgentDirectory(m.Home, id), "delivered.json")
}
func (m *Manager) briefPath(id string) string {
	return filepath.Join(storagepath.AgentDirectory(m.Home, id), "brief.md")
}
func atomicJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".daemon-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if err = json.NewEncoder(f).Encode(value); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func (m *Manager) save(a *managed) error {
	if _, err := storagepath.EnsureAgentDirectory(m.Home, a.def.AgentID); err != nil {
		return err
	}
	return atomicJSON(m.definitionPath(a.def.AgentID), a.def)
}
func (m *Manager) loadDelivered(a *managed) error {
	bytes, err := os.ReadFile(m.deliveredPath(a.def.AgentID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(bytes, &a.delivered); err != nil {
		return fmt.Errorf("invalid delivered set: %w", err)
	}
	if len(a.delivered) > 500 {
		a.delivered = a.delivered[len(a.delivered)-500:]
	}
	return nil
}
func (m *Manager) acquire(a *managed) error {
	if a.lock != nil {
		return nil
	}
	l, err := agentlock.Acquire(m.Home, a.def.AgentID)
	if err != nil {
		return err
	}
	a.lock = l
	return nil
}
func (m *Manager) release(a *managed) {
	if a.lock != nil {
		_ = a.lock.Release()
		a.lock = nil
	}
}
func (m *Manager) boot(ctx context.Context) error {
	paths, err := filepath.Glob(filepath.Join(storagepath.AgentsDirectory(m.Home), "*", "daemon.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := m.bootAgent(ctx, path); err != nil {
			// One damaged definition must not prevent unrelated agents from
			// starting. Do not rewrite or mark the rejected definition.
			log.Printf("supervisor: skipping %s: %v", path, err)
		}
	}
	return nil
}
func (m *Manager) bootAgent(ctx context.Context, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var def AgentDefinition
	if err = json.Unmarshal(data, &def); err != nil {
		return err
	}
	if err = validate(def); err != nil {
		return err
	}
	if path != m.definitionPath(def.AgentID) {
		return fmt.Errorf("definition ID does not match path")
	}
	if _, exists := m.agents[def.Name]; exists {
		return fmt.Errorf("duplicate agent name %s", def.Name)
	}
	a := &managed{def: def}
	if err = m.loadDelivered(a); err != nil {
		return err
	}
	if def.Desired == "running" && def.State != "stopped-by-dashboard" && def.State != "crashed" {
		if err = m.acquire(a); err != nil {
			return err
		}
		if err = m.watch(ctx, a); err != nil {
			m.release(a)
			return err
		}
	}
	m.agents[def.Name] = a
	return nil
}
func validate(d AgentDefinition) error {
	if !validName(d.Name) || d.AgentID == "" || d.Workstream == "" || d.WorkFolder == "" || !filepath.IsAbs(d.WorkFolder) {
		return fmt.Errorf("invalid agent definition")
	}
	if d.Desired != "running" && d.Desired != "stopped" {
		return fmt.Errorf("invalid desired state")
	}
	if d.Harness != "" && d.Harness != "pi" || d.Mode != "" && d.Mode != "tmux" {
		return fmt.Errorf("unsupported harness or mode")
	}
	return nil
}
func (m *Manager) Start(ctx context.Context, def AgentDefinition) error {
	if !validName(def.Name) || def.AgentID == "" || def.Workstream == "" || !filepath.IsAbs(def.WorkFolder) {
		return fmt.Errorf("invalid agent definition")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[def.Name]
	if a != nil && a.def.AgentID != def.AgentID {
		return fmt.Errorf("agent name already belongs to another ID")
	}
	if a == nil {
		a = &managed{}
		m.agents[def.Name] = a
	}
	old := a.def
	def.Version = 1
	def.Harness = "pi"
	def.Mode = "tmux"
	def.Desired = "running"
	def.State = "starting"
	def.Crashes = nil
	def.LastExit = nil
	// Explicit start after Stop starts fresh; an already-running agent is adopted.
	a.def = def
	a.nextStart = time.Time{}
	a.failures = 0
	if err := m.acquire(a); err != nil {
		a.def = old
		return err
	}
	if err := m.save(a); err != nil {
		return err
	}
	pane, err := m.Tmux.Inspect(ctx, def.Name)
	if err != nil {
		return err
	}
	if pane.Exists && pane.Dead {
		return m.launch(ctx, a, false)
	}
	return m.watch(ctx, a)
}
func (m *Manager) Stop(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[name]
	if a == nil {
		return os.ErrNotExist
	}
	a.def.Desired = "stopped"
	a.def.State = "stopped"
	a.nextStart = time.Time{}
	if err := m.save(a); err != nil {
		return err
	}
	if err := m.Tmux.Kill(ctx, name); err != nil {
		return err
	}
	m.release(a)
	return nil
}
func (m *Manager) Remove(ctx context.Context, name string) error {
	if err := m.Stop(ctx, name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[name]
	for _, path := range []string{m.definitionPath(a.def.AgentID), m.deliveredPath(a.def.AgentID), m.briefPath(a.def.AgentID)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	delete(m.agents, name)
	return nil
}
func (m *Manager) List(ctx context.Context) ([]AgentStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var list []AgentStatus
	for _, a := range m.agents {
		list = append(list, AgentStatus{Name: a.def.Name, AgentID: a.def.AgentID, Workstream: a.def.Workstream, Desired: a.def.Desired, State: a.def.State, PID: a.pid, LastExit: a.def.LastExit, LastPollAt: a.lastPoll})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}
func (m *Manager) Shutdown(ctx context.Context, stopAgents bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.agents {
		if stopAgents {
			if err := m.Tmux.Kill(ctx, a.def.Name); err != nil {
				return err
			}
			a.def.State = "stopped"
			if err := m.save(a); err != nil {
				return err
			}
		}
		m.release(a)
	}
	return nil
}
func (m *Manager) Run(ctx context.Context) error {
	m.mu.Lock()
	err := m.boot(ctx)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	close(m.booted)
	defer m.Shutdown(context.Background(), false)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := m.Tick(ctx); err != nil {
				return err
			}
		}
	}
}
func backoff(count int) time.Duration {
	switch {
	case count <= 1:
		return 5 * time.Second
	case count == 2:
		return 30 * time.Second
	default:
		return 2 * time.Minute
	}
}
func (m *Manager) watch(ctx context.Context, a *managed) error {
	now := m.now()
	pane, err := m.Tmux.Inspect(ctx, a.def.Name)
	if err != nil {
		return err
	}
	if pane.Exists && !pane.Dead {
		a.pid = pane.PID
		a.def.State = "running"
		if a.def.SessionStartedAt == "" {
			a.def.SessionStartedAt = now.Format(time.RFC3339Nano)
		}
		a.nextPoll = now
		return m.save(a)
	}
	if pane.Exists && pane.Dead {
		// A restart during backoff must not count the same dead pane twice.
		// The last exit timestamp is the durable restart timer.
		if a.def.State == "starting" && a.def.LastExit != nil {
			if at, parseErr := time.Parse(time.RFC3339Nano, a.def.LastExit.At); parseErr == nil {
				a.nextStart = at.Add(backoff(len(a.def.Crashes)))
				return nil
			}
		}
		a.def.LastExit = &Exit{At: now.Format(time.RFC3339Nano), Code: pane.ExitCode}
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
			return m.save(a)
		}
		a.nextStart = now.Add(backoff(len(kept)))
		a.def.State = "starting"
		return m.save(a)
	}
	return m.launch(ctx, a, false)
}
func (m *Manager) launch(ctx context.Context, a *managed, resume bool) error {
	if err := m.Tmux.Kill(ctx, a.def.Name); err != nil {
		return err
	}
	args := []string{m.Pi, "--aircommand-workstream", a.def.Workstream, "--aircommand-agent", a.def.AgentID, "--aircommand-cli", m.CLI, "--append-system-prompt", m.briefPath(a.def.AgentID)}
	if resume {
		args = append(args, "--continue")
	}
	args = append(args, "Check your unread AirCommand messages with aircom inbox and handle them.")
	if err := m.Tmux.Start(ctx, a.def, args); err != nil {
		return err
	}
	a.def.State = "running"
	a.def.SessionStartedAt = m.now().Format(time.RFC3339Nano)
	a.nextStart = time.Time{}
	a.nextPoll = m.now()
	a.pid = 0
	return m.save(a)
}

// Tick makes one lifecycle and notification pass; a fake clock makes restart
// and polling tests deterministic without sleeping.
func (m *Manager) Tick(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.agents {
		if a.def.Desired != "running" || a.def.State == "crashed" || a.def.State == "stopped-by-dashboard" {
			continue
		}
		if a.lock == nil {
			if err := m.acquire(a); err != nil {
				return err
			}
		}
		pane, err := m.Tmux.Inspect(ctx, a.def.Name)
		if err != nil {
			return err
		}
		if pane.Exists && pane.Dead && a.nextStart.IsZero() {
			if err := m.watch(ctx, a); err != nil {
				return err
			}
			continue
		}
		if !a.nextStart.IsZero() {
			if m.now().Before(a.nextStart) {
				continue
			}
			if err := m.launch(ctx, a, true); err != nil {
				return err
			}
			continue
		}
		if !pane.Exists {
			if err := m.watch(ctx, a); err != nil {
				return err
			}
			continue
		}
		a.pid = pane.PID
		if m.Poll != nil && !m.now().Before(a.nextPoll) {
			if err := m.poll(ctx, a); err != nil {
				return err
			}
		}
	}
	return nil
}
func (m *Manager) poll(ctx context.Context, a *managed) error {
	store := listenstore.NewStore(m.Home)
	cred, err := credentials.NewStore(m.Home).FindByAgent(a.def.Workstream, a.def.AgentID)
	if err != nil {
		return err
	}
	key := cred.WorkstreamKey()
	cursor, has, err := store.LoadCursor(a.def.AgentID, key)
	if err != nil {
		return err
	}
	feed, err := m.Poll.Fetch(ctx, a.def, cursor, has)
	if errors.Is(err, ErrAgentStopped) {
		a.def.State = "stopped-by-dashboard"
		if err := m.save(a); err != nil {
			return err
		}
		if err := m.Tmux.Kill(ctx, a.def.Name); err != nil {
			return err
		}
		m.release(a)
		return nil
	}
	if err != nil {
		a.failures++
		a.nextPoll = m.now().Add(backoff(a.failures))
		return nil
	}
	a.failures = 0
	if has {
		for _, n := range feed.Notifications {
			found := false
			for _, id := range a.delivered {
				if id == n.MessageID {
					found = true
					break
				}
			}
			if found {
				continue
			}
			spooled, err := m.Poll.Spool(ctx, a.def, n)
			if err != nil {
				return err
			}
			if err = store.AppendNotification(a.def.AgentID, spooled); err != nil {
				return err
			}
			a.delivered = append(a.delivered, n.MessageID)
			if len(a.delivered) > 500 {
				a.delivered = a.delivered[len(a.delivered)-500:]
			}
			if err = atomicJSON(m.deliveredPath(a.def.AgentID), a.delivered); err != nil {
				return err
			}
		}
	}
	if !has || cursor != feed.Cursor {
		if err = store.SaveCursor(a.def.AgentID, key, feed.Cursor); err != nil {
			return err
		}
	}
	a.lastPoll = m.now().Format(time.RFC3339Nano)
	if feed.PollAfter <= 0 {
		feed.PollAfter = 30 * time.Second
	}
	a.nextPoll = m.now().Add(feed.PollAfter)
	return nil
}
