package supervisor

import (
	"context"
	"errors"
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
		if a.def.Kind == "attached" {
			continue
		}
		d := a.def
		d.Repos = append([]string(nil), d.Repos...)
		out = append(out, d)
	}
	return out
}

// Stage saves a new stopped definition without launching a pi session.
func (m *Manager) Stage(def AgentDefinition) error {
	def.Version, def.Kind, def.Harness, def.Desired, def.State = 1, "started", "pi", "stopped", "stopped"
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
	if a.def.Kind == "attached" {
		return fmt.Errorf("attached agents have no server definition revision")
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

// ErrControlBusy returns immediately instead of letting a 10-second CLI
// socket deadline expire while reconciliation performs slow network/git work.
var ErrControlBusy = errors.New("machine control busy; retry shortly")

// LockLocalChange serializes the local action and its desired-state post with
// reconciliation. The control socket calls it before changing local state.
func (m *Manager) LockLocalChange() (func(), error) {
	if m.OperationGate == nil {
		return func() {}, nil
	}
	if !m.OperationGate.TryLock() {
		return nil, ErrControlBusy
	}
	return m.OperationGate.Unlock, nil
}

func permanentDesiredError(err error) bool {
	var status interface{ HTTPStatus() int }
	return errors.As(err, &status) && (status.HTTPStatus() == 400 || status.HTTPStatus() == 404 || status.HTTPStatus() == 409)
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
	if def.Kind == "attached" {
		return nil
	}
	if post == nil {
		return nil
	}
	revision, err := post(ctx, def)
	if err != nil {
		if permanentDesiredError(err) {
			log.Printf("supervisor: dropping rejected desired post for %s: %v", name, err)
			return nil
		}
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

// FlushDesired retries each pending local post independently. A transient
// failure skips only that agent for this check; other agents still reconcile.
// Callers hold OperationGate during the check, so no local action races a post.
func (m *Manager) FlushDesired(ctx context.Context) map[string]bool {
	m.mu.Lock()
	names := make([]string, 0, len(m.pendingDesired))
	for name := range m.pendingDesired {
		names = append(names, name)
	}
	m.mu.Unlock()
	skip := make(map[string]bool)
	for _, name := range names {
		m.mu.Lock()
		a, post := m.agents[name], m.DesiredPost
		var def AgentDefinition
		if a != nil {
			def = a.def
		} else {
			delete(m.pendingDesired, name)
		}
		m.mu.Unlock()
		if a == nil {
			continue
		} // removed locally; no agent left to reconcile
		if def.Kind == "attached" {
			m.mu.Lock()
			delete(m.pendingDesired, name)
			m.mu.Unlock()
			continue
		}
		if post == nil {
			skip[def.AgentID] = true
			continue
		}
		revision, err := post(ctx, def)
		if err != nil {
			if permanentDesiredError(err) {
				m.mu.Lock()
				delete(m.pendingDesired, name)
				m.mu.Unlock()
				log.Printf("supervisor: dropping rejected desired post for %s: %v", name, err)
			} else {
				skip[def.AgentID] = true
				log.Printf("supervisor: desired post for %s not delivered (retrying): %v", name, err)
			}
			continue
		}
		if err := m.MarkRevision(name, revision); err != nil {
			skip[def.AgentID] = true
			log.Printf("supervisor: desired revision for %s not saved (retrying): %v", name, err)
			continue
		}
		m.mu.Lock()
		delete(m.pendingDesired, name)
		m.mu.Unlock()
	}
	return skip
}
