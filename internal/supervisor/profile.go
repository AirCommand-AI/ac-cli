package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxRolePromptBytes caps the role prompt the server can put in a brief.
const MaxRolePromptBytes = 16 * 1024

// piProvider is the pi provider every run machine and laptop agent uses.
const piProvider = "openai-codex"

var profileToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

// AgentProfile is the server's role and model settings for a started agent.
// The daemon writes the brief from it before every start; a change never
// restarts a running agent.
type AgentProfile struct {
	Role             string `json:"role,omitempty"`
	RoleLabel        string `json:"roleLabel,omitempty"`
	RolePrompt       string `json:"rolePrompt,omitempty"`
	RolePromptRev    int64  `json:"rolePromptRev,omitempty"`
	Model            string `json:"model,omitempty"`
	Effort           string `json:"effort,omitempty"`
	OrganizationName string `json:"organizationName,omitempty"`
}

func (p *AgentProfile) valid() bool {
	if p == nil {
		return true
	}
	for _, v := range []string{p.Role, p.Model, p.Effort} {
		if v != "" && !profileToken.MatchString(v) {
			return false
		}
	}
	for _, v := range []string{p.RoleLabel, p.OrganizationName} {
		if len(v) > 200 || strings.ContainsAny(v, "\r\n") || !utf8.ValidString(v) {
			return false
		}
	}
	return len(p.RolePrompt) <= MaxRolePromptBytes && utf8.ValidString(p.RolePrompt) && p.RolePromptRev >= 0
}

func (p *AgentProfile) equal(o *AgentProfile) bool {
	if p == nil || o == nil {
		return p == nil && o == nil
	}
	return *p == *o
}

// UpdateProfile saves the server's role and model settings for an agent
// without restarting it. It reports whether anything changed.
func (m *Manager) UpdateProfile(name string, p *AgentProfile) (bool, error) {
	if !p.valid() {
		return false, fmt.Errorf("invalid agent profile")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.agents[name]
	if a == nil {
		return false, os.ErrNotExist
	}
	if a.def.Kind == "attached" || a.def.Profile.equal(p) {
		return false, nil
	}
	previous := a.def.Profile
	a.def.Profile = p
	if err := m.save(a); err != nil {
		a.def.Profile = previous
		return false, err
	}
	return true, nil
}

// briefText is the agent's brief: who it is, where it works, and its role's
// instructions when it has a role.
func briefText(def AgentDefinition) string {
	workspace := def.Organization
	who := def.Name
	var p AgentProfile
	if def.Profile != nil {
		p = *def.Profile
	}
	if p.OrganizationName != "" {
		workspace = p.OrganizationName
	}
	if p.RoleLabel != "" {
		who = fmt.Sprintf("%s, the %s", def.Name, p.RoleLabel)
	}
	folder := def.WorkFolder
	if folder == "" {
		folder = "~/work/" + def.Name
	}
	text := fmt.Sprintf("You are %s in workstream %s (workspace %s). Work only in %s.\nWhen you start, read your unread AirCommand messages (aircom inbox) and handle them.\n", who, def.Workstream, workspace, folder)
	if p.Role != "" && strings.TrimSpace(p.RolePrompt) != "" {
		text += "\n" + strings.TrimRight(p.RolePrompt, "\n") + "\n"
	}
	return text
}

// writeBrief writes brief.md before a start, so every start (tmux, headless,
// takeover, crash restart) uses the current role.
func (m *Manager) writeBrief(def AgentDefinition) error {
	path := m.briefPath(def.AgentID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".brief-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(briefText(def)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// piProfileArgs returns pi's --model and --thinking flags for the agent.
func piProfileArgs(def AgentDefinition) []string {
	if def.Profile == nil {
		return nil
	}
	var args []string
	if def.Profile.Model != "" {
		args = append(args, "--model", piProvider+"/"+def.Profile.Model)
	}
	if def.Profile.Effort != "" {
		args = append(args, "--thinking", def.Profile.Effort)
	}
	return args
}
