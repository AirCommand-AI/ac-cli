package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	claudecodeadapter "github.com/AirCommand-AI/ac-cli/adapters/claude-code"
	"github.com/AirCommand-AI/ac-cli/internal/claudeauth"
	"github.com/AirCommand-AI/ac-cli/internal/claudedriver"
	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

// HarnessClaude runs the agent with headless Claude Code (design-claude-code-driver).
const HarnessClaude = "claude-code"

// ClaudeAuth is the machine's Claude credentials (package claudeauth).
type ClaudeAuth = claudeauth.Auth

// ErrClaudeNotConnected: the machine has no Claude token.
var ErrClaudeNotConnected = errors.New("Claude is not connected on this machine; connect it in Integrations")

func isClaude(def AgentDefinition) bool { return def.Harness == HarnessClaude }

func (m *Manager) claudeConfigDir(id string) string {
	return filepath.Join(storagepath.AgentDirectory(m.Home, id), "claude")
}

// blockedClaudeEnv are variables that would make Claude Code use something
// other than the subscription token given here.
var blockedClaudeEnv = []string{"ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "CLAUDE_CODE_OAUTH_TOKEN=", "ANTHROPIC_BASE_URL=", "ANTHROPIC_CUSTOM_HEADERS=", "CLAUDE_CONFIG_DIR=", "CLAUDE_CODE_USE_BEDROCK=", "CLAUDE_CODE_USE_VERTEX="}

// claudeEnv is the daemon's environment without any Anthropic credential,
// plus the subscription token and the Multi-Router headers.
func claudeEnv(base []string, configDir string, auth *ClaudeAuth) []string {
	out := make([]string, 0, len(base)+8)
	for _, kv := range base {
		blocked := false
		for _, prefix := range blockedClaudeEnv {
			blocked = blocked || strings.HasPrefix(kv, prefix)
		}
		if !blocked {
			out = append(out, kv)
		}
	}
	return append(out,
		"CLAUDE_CONFIG_DIR="+configDir,
		"CLAUDE_CODE_OAUTH_TOKEN="+auth.OAuthToken,
		"ANTHROPIC_BASE_URL="+auth.BaseURL,
		"ANTHROPIC_CUSTOM_HEADERS=X-MR-Router: "+auth.RouterID+"\nX-MR-Api-Token: "+auth.APITokenHeader,
		"DISABLE_AUTOUPDATER=1",
	)
}

// claudeArgs are the flags after the session flag.
func (m *Manager) claudeArgs(def AgentDefinition) []string {
	args := []string{}
	if def.Profile != nil && def.Profile.Model != "" {
		args = append(args, "--model", def.Profile.Model)
	}
	if def.Profile != nil && def.Profile.Effort != "" {
		args = append(args, "--effort", def.Profile.Effort)
	}
	return append(args, "--append-system-prompt-file", m.briefPath(def.AgentID), "--dangerously-skip-permissions")
}

// launchSpec builds the driver launch for the agent's harness.
func (m *Manager) launchSpec(def AgentDefinition) (pidriver.LaunchSpec, error) {
	if !isClaude(def) {
		args := []string{"--aircommand-headless", "--aircommand-workstream", def.Workstream, "--aircommand-agent", def.AgentID, "--aircommand-cli", m.CLI, "--append-system-prompt", m.briefPath(def.AgentID)}
		return pidriver.LaunchSpec{PiPath: m.Pi, WorkDir: def.WorkFolder, SessionID: def.AgentID, Args: append(args, piProfileArgs(def)...)}, nil
	}
	if m.ClaudeAuth == nil {
		return pidriver.LaunchSpec{}, ErrClaudeNotConnected
	}
	auth, err := m.ClaudeAuth()
	if err != nil || !auth.Valid() {
		return pidriver.LaunchSpec{}, ErrClaudeNotConnected
	}
	session, err := claudedriver.SessionUUID(def.AgentID)
	if err != nil {
		return pidriver.LaunchSpec{}, err
	}
	config := m.claudeConfigDir(def.AgentID)
	if err := os.MkdirAll(config, 0o700); err != nil {
		return pidriver.LaunchSpec{}, err
	}
	return pidriver.LaunchSpec{PiPath: m.claudePath(), WorkDir: def.WorkFolder, SessionID: session, Args: m.claudeArgs(def), Env: claudeEnv(os.Environ(), config, auth)}, nil
}

func (m *Manager) claudePath() string {
	if m.Claude != "" {
		return m.Claude
	}
	return "claude"
}

// checkHarness verifies the harness binary before a launch.
func (m *Manager) checkHarness(def AgentDefinition) error {
	if !isClaude(def) {
		return nil
	}
	if _, err := exec.LookPath(m.claudePath()); err != nil {
		return fmt.Errorf("Claude Code is required for this agent and is not installed: %w", err)
	}
	return nil
}

func (m *Manager) newDriver(def AgentDefinition, log io.Writer) pidriver.Driver {
	if isClaude(def) {
		if m.NewClaudeDriver != nil {
			return m.NewClaudeDriver(log)
		}
		return claudedriver.New(pidriver.Options{Log: log})
	}
	if m.NewDriver == nil {
		return nil
	}
	return m.NewDriver(log)
}

// brief adds the AirCommand guidance for Claude Code agents (pi gets it from
// its add-on).
func (m *Manager) brief(def AgentDefinition) string {
	text := briefText(def)
	if isClaude(def) {
		text += "\n" + claudecodeadapter.HeadlessGuidance(m.CLI)
	}
	return text
}

// AgentReporter reports for agents without an add-on (Claude Code): the
// runtime they actually use and each turn's token usage.
type AgentReporter interface {
	ReportRuntime(ctx context.Context, def AgentDefinition, runtime map[string]any) error
	ReportUsage(ctx context.Context, def AgentDefinition, turnID string, in, out int64) error
}

// reportClaudeEvent reports runtime on each turn start and usage on each
// turn end. Called with m.mu held; the network calls run without it.
func (m *Manager) reportClaudeEvent(a *managed, e pidriver.Event) {
	reporter, ok := m.Poll.(AgentReporter)
	if !ok || !isClaude(a.def) {
		return
	}
	def := a.def
	switch e.Kind {
	case "agent_start":
		host, _ := os.Hostname()
		runtime := map[string]any{"harness": HarnessClaude, "provider": "anthropic", "cwd": def.WorkFolder, "hostname": host, "machine": host, "sessionStartedAt": def.SessionStartedAt}
		if v, ok := e.Data["claude_code_version"].(string); ok {
			runtime["harnessVersion"] = v
		}
		if v, ok := e.Data["model"].(string); ok {
			runtime["model"] = v
		}
		if def.Profile != nil && def.Profile.Effort != "" {
			runtime["effort"] = def.Profile.Effort
		}
		if a.driver != nil {
			runtime["pid"] = a.driver.State().PID
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := reporter.ReportRuntime(ctx, def, runtime); err != nil {
				log.Printf("supervisor: agent %s: runtime report: %v", def.Name, err)
			}
		}()
	case "agent_settled":
		usage, _ := e.Data["usage"].(map[string]any)
		in, out := tokens(usage, "input_tokens")+tokens(usage, "cache_creation_input_tokens")+tokens(usage, "cache_read_input_tokens"), tokens(usage, "output_tokens")
		if in == 0 && out == 0 {
			return
		}
		turn, _ := e.Data["uuid"].(string)
		if turn == "" {
			turn = fmt.Sprintf("%s-%d", def.AgentID, e.At.UnixNano())
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := reporter.ReportUsage(ctx, def, turn, in, out); err != nil {
				log.Printf("supervisor: agent %s: usage report: %v", def.Name, err)
			}
		}()
	}
}

func tokens(usage map[string]any, key string) int64 {
	if v, ok := usage[key].(float64); ok && v > 0 {
		return int64(v)
	}
	return 0
}
