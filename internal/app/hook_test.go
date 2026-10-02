package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func TestHookListenerFindsTheListenerInTheSameClaudeSession(t *testing.T) {
	table := []hookProcess{
		{pid: 100, ppid: 1, command: "/bin/zsh -l"},
		{pid: 200, ppid: 100, command: "claude --dangerously-skip-permissions"},
		{pid: 210, ppid: 200, command: "/bin/zsh -c source snapshot.sh && aircom join"},
		{pid: 211, ppid: 210, command: "/Users/x/.local/bin/aircom join --agent mr-lead --org Multi Router --workstream 883 --listen"},
		{pid: 220, ppid: 200, command: "/bin/sh -c aircom hook claude-code working"},
		{pid: 221, ppid: 220, command: "aircom hook claude-code working"},
		{pid: 300, ppid: 100, command: "claude"},
		{pid: 311, ppid: 300, command: "aircom listen --workstream 477 --agent other"},
	}
	tests := []struct {
		name      string
		self      int
		agent     string
		code      string
		wantFound bool
	}{
		{name: "own session", self: 221, agent: "mr-lead", code: "883", wantFound: true},
		{name: "no claude ancestor", self: 100, wantFound: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent, code, ok := hookListener(table, test.self)
			if ok != test.wantFound || agent != test.agent || code != test.code {
				t.Fatalf("hookListener = %q %q %v, want %q %q %v", agent, code, ok, test.agent, test.code, test.wantFound)
			}
		})
	}
}

func TestHookListenerRefusesTwoAgentsInOneSession(t *testing.T) {
	table := []hookProcess{
		{pid: 200, ppid: 1, command: "claude"},
		{pid: 211, ppid: 200, command: "aircom join --agent a --workstream 1 --listen"},
		{pid: 212, ppid: 200, command: "aircom listen --agent b --workstream 1"},
		{pid: 221, ppid: 200, command: "aircom hook claude-code idle"},
	}
	if _, _, ok := hookListener(table, 221); ok {
		t.Fatal("two listeners in one session must not be guessed between")
	}
}

func TestListenerArgs(t *testing.T) {
	tests := []struct {
		command string
		agent   string
		code    string
		ok      bool
	}{
		{"aircom join --agent mr-eng-1 --workstream 883 --listen", "mr-eng-1", "883", true},
		{"aircom join --agent mr-eng-1 --workstream 883", "", "", false},
		{"/usr/local/bin/aircom listen --workstream 477 --agent agm_x", "agm_x", "477", true},
		{"aircom join --agent placed --listen", "placed", "", true},
		{"aircom inbox --agent x --workstream 1", "", "", false},
	}
	for _, test := range tests {
		agent, code, ok := listenerArgs(test.command)
		if ok != test.ok || (ok && (agent != test.agent || code != test.code)) {
			t.Errorf("listenerArgs(%q) = %q %q %v", test.command, agent, code, ok)
		}
	}
}

func TestLastTranscriptListenerUsesTheLatestJoinAndHonoursLeave(t *testing.T) {
	dir := t.TempDir()
	write := func(lines ...string) string {
		path := filepath.Join(dir, "t.jsonl")
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	tool := func(command string) string {
		return `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Monitor","input":{"command":"` + command + `"}}]}}`
	}
	tests := []struct {
		name  string
		lines []string
		agent string
		code  string
	}{
		{"join then rejoin", []string{tool("~/.local/bin/aircom join --agent mr-lead --org \\\"Multi Router\\\" --workstream 477 --listen"), tool("aircom leave --agent mr-lead"), tool("~/.local/bin/aircom join --agent mr-lead --workstream 883 --listen")}, "mr-lead", "883"},
		{"quoted org keeps the workstream", []string{tool("aircom join --agent mr-lead --org \\\"Multi Router\\\" --workstream 883 --listen")}, "mr-lead", "883"},
		{"left", []string{tool("aircom listen --workstream 883 --agent agm_a"), tool("aircom leave --agent agm_a")}, "", ""},
		{"text mention is not a command", []string{`{"type":"user","message":{"content":"run aircom join --agent x --workstream 1 --listen"}}`}, "", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent, code := lastTranscriptListener(write(test.lines...))
			if agent != test.agent || code != test.code {
				t.Fatalf("got %q %q, want %q %q", agent, code, test.agent, test.code)
			}
		})
	}
}

func TestHookNotesLiveUnderDotAircommand(t *testing.T) {
	home := t.TempDir()
	a := &App{Store: credentials.NewStore(home)}
	if !a.hookStateDue("agm_a", "883", "working") || a.hookStateDue("agm_a", "883", "working") {
		t.Fatal("a repeated state within a minute must be throttled")
	}
	if !a.hookStateDue("agm_a", "883", "idle") {
		t.Fatal("a changed state must be sent")
	}
	if _, err := os.Stat(filepath.Join(home, ".aircommand", "hooks", "883-agm_a.json")); err != nil {
		t.Fatalf("throttle note not under ~/.aircommand/hooks: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "hooks")); !os.IsNotExist(err) {
		t.Fatal("hook notes must not be written to the home folder itself")
	}
}
