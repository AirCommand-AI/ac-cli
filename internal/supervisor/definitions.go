package supervisor

import (
	"context"
	"fmt"
	"log"
	"os"
)

// Definitions returns a snapshot. The reconciler never holds Manager.mu
// across network requests, git operations or lifecycle calls.
func (m *Manager) Definitions() []AgentDefinition {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AgentDefinition, 0, len(m.agents))
	for _, a := range m.agents {
		d := a.def
		d.Repos = append([]string(nil), d.Repos...)
		out = append(out, d)
	}
	return out
}

// Stage saves a new stopped definition without launching a pi session.
func (m *Manager) Stage(def AgentDefinition) error {
	def.Version, def.Harness, def.Desired, def.State = 1, "pi", "stopped", "stopped"
	if err := validate(def); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agents[def.Name] != nil {
		return fmt.Errorf("agent already exists")
	}
	a := &managed{def: def}
	if err := m.save(a); err != nil {
		return err
	}
	m.agents[def.Name] = a
	return nil
}

// MarkRevision durably records the server revision after a successful apply.
func (m *Manager) MarkRevision(name string, revision int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[name]
	if a == nil {
		return os.ErrNotExist
	}
	if revision <= a.def.Revision {
		return nil
	}
	previous := a.def.Revision
	a.def.Revision = revision
	if err := m.save(a); err != nil {
		a.def.Revision = previous
		return err
	}
	return nil
}

// LockLocalChange serializes the local action and its desired-state post with
// reconciliation. The control socket calls it before changing local state.
func (m *Manager) LockLocalChange() func() {
	if m.OperationGate == nil {
		return func() {}
	}
	m.OperationGate.Lock()
	return m.OperationGate.Unlock
}

// PostDesired retries transient server errors on the next check-in. The local
// lifecycle change has already succeeded and must not be reported as failed.
func (m *Manager) PostDesired(ctx context.Context, name string) error {
	m.mu.Lock()
	a := m.agents[name]
	var def AgentDefinition
	if a != nil {
		def = a.def
	}
	post := m.DesiredPost
	m.mu.Unlock()
	if a == nil {
		return os.ErrNotExist
	}
	if post == nil {
		return nil
	}
	revision, err := post(ctx, def)
	if err != nil {
		m.mu.Lock()
		if m.pendingDesired == nil {
			m.pendingDesired = make(map[string]bool)
		}
		m.pendingDesired[name] = true
		m.mu.Unlock()
		log.Printf("supervisor: desired state for %s not posted (retrying): %v", name, err)
		return nil
	}
	if err := m.MarkRevision(name, revision); err != nil {
		m.mu.Lock()
		if m.pendingDesired == nil {
			m.pendingDesired = make(map[string]bool)
		}
		m.pendingDesired[name] = true
		m.mu.Unlock()
		log.Printf("supervisor: desired revision for %s not saved (retrying): %v", name, err)
		return nil
	}
	m.mu.Lock()
	delete(m.pendingDesired, name)
	m.mu.Unlock()
	return nil
}

// FlushDesired runs before fetching remote definitions. On failure, do not
// reconcile an older server revision over a newer local change.
func (m *Manager) FlushDesired(ctx context.Context) error {
	m.mu.Lock()
	names := make([]string, 0, len(m.pendingDesired))
	for name := range m.pendingDesired {
		names = append(names, name)
	}
	m.mu.Unlock()
	for _, name := range names {
		m.mu.Lock()
		a, post := m.agents[name], m.DesiredPost
		var def AgentDefinition
		if a != nil {
			def = a.def
		}
		m.mu.Unlock()
		if a == nil || post == nil {
			return fmt.Errorf("pending desired post unavailable for %s", name)
		}
		revision, err := post(ctx, def)
		if err != nil {
			return fmt.Errorf("pending desired post for %s: %w", name, err)
		}
		if err := m.MarkRevision(name, revision); err != nil {
			return err
		}
		m.mu.Lock()
		delete(m.pendingDesired, name)
		m.mu.Unlock()
	}
	return nil
}
