package supervisor

import (
	"context"
	"fmt"
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

// PostDesired is called by local daemon control operations after a successful
// lifecycle change. The function is configured by the executable, never by
// a remote check-in frame.
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
		return err
	}
	return m.MarkRevision(name, revision)
}
