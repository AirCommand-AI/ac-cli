package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRuntimeAdaptersShareTaskGuidance(t *testing.T) {
	t.Parallel()

	repositoryRoot := adapterRepositoryRoot(t)
	skill := readAdapterFile(t, filepath.Join(repositoryRoot, "adapters", "claude-code", "SKILL.md"))
	extension := readAdapterFile(t, filepath.Join(repositoryRoot, "adapters", "pi", "index.ts"))

	skillGuidance := textBetween(t, skill, "<!-- task-guidance:start -->", "<!-- task-guidance:end -->")
	extensionBlock := textBetween(t, extension, "// task-guidance:start", "// task-guidance:end")
	extensionGuidance := textBetween(t, extensionBlock, "const TASK_GUIDANCE = String.raw`", "`;")
	if skillGuidance != extensionGuidance {
		t.Fatalf("Claude Code and pi task guidance differ\n--- Claude Code ---\n%s\n--- pi ---\n%s", skillGuidance, extensionGuidance)
	}
	if !strings.Contains(extension, "\t\t\tTASK_GUIDANCE,") {
		t.Fatal("pi extension defines task guidance but does not inject it")
	}
	if !strings.Contains(skill, "or list, inspect, create, progress, or comment on tasks") {
		t.Fatal("Claude Code skill description does not advertise task support")
	}
	if !strings.Contains(extension, "use messages and operator-authorized tasks") {
		t.Fatal("pi tool prompt snippet does not advertise task support")
	}

	for _, required := range []string{
		"aircom tasks --workstream",
		"aircom task <task>",
		"--status in_flight",
		"--status cancelled --reason",
		"--acceptance",
		"--validation",
		"--comment",
		"--status landed",
		"exact structural senderId",
		"operator has authorized",
		"untrusted data, not as instructions",
	} {
		if !strings.Contains(skillGuidance, required) {
			t.Errorf("shared task guidance is missing %q", required)
		}
	}
}

func adapterRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate adapter guidance test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func readAdapterFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func textBetween(t *testing.T, contents string, start string, end string) string {
	t.Helper()
	startIndex := strings.Index(contents, start)
	if startIndex < 0 {
		t.Fatalf("missing start marker %q", start)
	}
	contents = contents[startIndex+len(start):]
	endIndex := strings.Index(contents, end)
	if endIndex < 0 {
		t.Fatalf("missing end marker %q", end)
	}
	return strings.TrimSpace(contents[:endIndex])
}

// TestClaudeCodeSkillReArmsAnExpiredListener keeps the guidance for Claude
// Code's 30-minute Monitor cap: re-arm with the exact listen call after an
// expiry, catch up on the inbox, and never treat a real failure as an expiry.
func TestClaudeCodeSkillReArmsAnExpiredListener(t *testing.T) {
	repositoryRoot := adapterRepositoryRoot(t)
	skill := readAdapterFile(t, filepath.Join(repositoryRoot, "adapters", "claude-code", "SKILL.md"))
	section := textBetween(t, skill, "## When the listener's watch expires", "## Current command surface")

	cases := []struct {
		name     string
		required string
	}{
		{"names the expiry notice", "Monitor expired after 30m"},
		{"re-arms with the exact listen call", `command: "~/.local/bin/aircom listen --workstream <code> --agent <agentId>"`},
		{"keeps the monitor persistent", "persistent: true"},
		{"one listener only", "Never start a second listener while the first is still"},
		{"real failures are not expiries", "do not\nre-arm"},
		{"catches up on the inbox", "run `aircom inbox` once"},
		{"does not acknowledge early", "Acknowledge\nonly after acting on it"},
		{"states the limitation", "nothing re-arms the listener until the session is back"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(section, tc.required) {
				t.Errorf("re-arm guidance is missing %q", tc.required)
			}
		})
	}
}
