package supervisor

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/claudeauth"
	"github.com/AirCommand-AI/ac-cli/internal/claudedriver"
	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

const claudeAgentID = "agm_0123456789abcdef0123456789abcdef"

func claudeAuth() *claudeauth.Auth {
	return &claudeauth.Auth{OAuthToken: "sk-ant-oat01-secret", BaseURL: "https://api.multi-router.ai", RouterID: "rt_claude", APITokenHeader: "Bearer mr"}
}

func claudeSetup(t *testing.T, auth *claudeauth.Auth) (*Manager, *pidriver.Fake, AgentDefinition) {
	t.Helper()
	m, _, _, _ := setup(t)
	fake := pidriver.NewFake()
	m.NewClaudeDriver = func(io.Writer) pidriver.Driver { return fake }
	m.Claude = "/bin/sh" // any existing binary: LookPath must succeed
	m.ClaudeAuth = func() (*claudeauth.Auth, error) {
		if auth == nil {
			return nil, claudeauth.ErrMissing
		}
		return auth, nil
	}
	d := definition(m.Home)
	d.AgentID = claudeAgentID
	d.Name = "lead"
	d.Harness = HarnessClaude
	d.Mode = "headless"
	d.Profile = &AgentProfile{Role: "eng-lead", RoleLabel: "Eng Lead", RolePrompt: "Lead.", Model: "claude-opus-5-5", Effort: "max"}
	return m, fake, d
}

func TestClaudeAgentLaunch(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api03-must-not-leak")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "nope")
	m, fake, d := claudeSetup(t, claudeAuth())
	if err := m.Start(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(fake.Launches) != 1 {
		t.Fatalf("launches = %d", len(fake.Launches))
	}
	spec := fake.Launches[0]
	if spec.SessionID != "01234567-89ab-cdef-0123-456789abcdef" || spec.PiPath != "/bin/sh" {
		t.Fatalf("spec = %+v", spec)
	}
	for _, pair := range [][2]string{{"--model", "claude-opus-5-5"}, {"--effort", "max"}, {"--append-system-prompt-file", m.briefPath(d.AgentID)}} {
		i := slices.Index(spec.Args, pair[0])
		if i < 0 || spec.Args[i+1] != pair[1] {
			t.Fatalf("args %v missing %v", spec.Args, pair)
		}
	}
	if !slices.Contains(spec.Args, "--dangerously-skip-permissions") || slices.Contains(spec.Args, "--aircommand-headless") {
		t.Fatalf("args = %v", spec.Args)
	}
	env := strings.Join(spec.Env, "\n")
	for _, want := range []string{"CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-secret", "ANTHROPIC_BASE_URL=https://api.multi-router.ai", "ANTHROPIC_CUSTOM_HEADERS=X-MR-Router: rt_claude\nX-MR-Api-Token: Bearer mr", "CLAUDE_CONFIG_DIR=" + filepath.Join(filepath.Dir(m.definitionPath(d.AgentID)), "claude")} {
		if !strings.Contains(env, want) {
			t.Fatalf("env missing %q", want)
		}
	}
	if strings.Contains(env, "sk-ant-api03") || strings.Contains(env, "ANTHROPIC_AUTH_TOKEN=nope") {
		t.Fatal("API key variables reached Claude Code")
	}
	brief, err := os.ReadFile(m.briefPath(d.AgentID))
	if err != nil || !strings.Contains(string(brief), "the Eng Lead") || !strings.Contains(string(brief), "Lead.") || !strings.Contains(string(brief), "# AirCommand") || !strings.Contains(string(brief), "aircom inbox") {
		t.Fatalf("brief = %s %v", brief, err)
	}
	if _, err := m.Takeover(context.Background(), d.Name); err == nil || !strings.Contains(err.Error(), "not available for Claude Code") {
		t.Fatalf("takeover = %v", err)
	}
}

func TestClaudeAgentWaitsForToken(t *testing.T) {
	m, fake, d := claudeSetup(t, nil)
	if err := m.Start(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	a := m.agents[d.Name]
	if len(fake.Launches) != 0 || a.def.State != "starting" || !strings.Contains(a.def.Reason, "Claude is not connected") || a.nextStart.IsZero() {
		t.Fatalf("state = %s %q next=%v launches=%d", a.def.State, a.def.Reason, a.nextStart, len(fake.Launches))
	}
}

func TestClaudeAgentWithAPIKeyIsNotRestarted(t *testing.T) {
	m, fake, d := claudeSetup(t, claudeAuth())
	if err := m.Start(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	fake.ExitCh <- pidriver.Exit{Err: claudedriver.ErrAPIKey}
	a := m.agents[d.Name]
	m.mu.Lock()
	err := m.tickHeadless(context.Background(), a)
	m.mu.Unlock()
	if err != nil || a.def.State != "crashed" || !strings.Contains(a.def.Reason, "API key") {
		t.Fatalf("state = %s %q %v", a.def.State, a.def.Reason, err)
	}
}

func TestClaudeHarnessRules(t *testing.T) {
	m, _, d := claudeSetup(t, claudeAuth())
	d.Mode = "tmux"
	if err := m.Start(context.Background(), d); err == nil {
		t.Fatal("tmux Claude Code agent accepted")
	}
	d.Harness = "codex"
	d.Mode = "headless"
	if err := m.Start(context.Background(), d); err == nil {
		t.Fatal("unknown harness accepted")
	}
}
