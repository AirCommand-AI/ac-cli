package supervisor

import (
	"fmt"
	"os"
	"strings"
)

// AddRepo records a repository cloned into a running agent's work folder
// after the agent was created. The agent is not restarted. It reports
// whether the saved definition changed.
func (m *Manager) AddRepo(name, repo string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[name]
	if a == nil {
		return false, os.ErrNotExist
	}
	if a.def.Kind == "attached" {
		return false, fmt.Errorf("add repository to attached agent %s", name)
	}
	for _, existing := range a.def.Repos {
		if strings.EqualFold(existing, repo) {
			return false, nil
		}
	}
	previous := a.def.Repos
	a.def.Repos = append(append([]string(nil), previous...), repo)
	if err := m.save(a); err != nil {
		a.def.Repos = previous
		return false, fmt.Errorf("save repository %s for %s: %w", repo, name, err)
	}
	return true, nil
}
