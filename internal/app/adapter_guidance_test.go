package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPiAdapterReportsTurnUsage(t *testing.T) {
	repositoryRoot := adapterRepositoryRoot(t)
	extension := readAdapterFile(t, filepath.Join(repositoryRoot, "adapters", "pi", "index.ts"))
	for _, part := range []string{`pi.on("turn_end"`, `event.message.usage?.input`, `event.message.usage?.output`, `"usage"`, `messageEntryId`} {
		if !strings.Contains(extension, part) {
			t.Errorf("missing pi token reporting: %s", part)
		}
	}
}

func TestPiHeadlessFlagDisablesExtensionDeliveryAndReporting(t *testing.T) {
	extension := readAdapterFile(t, filepath.Join(adapterRepositoryRoot(t), "adapters", "pi", "index.ts"))
	for _, part := range []string{
		`pi.registerFlag(HEADLESS_FLAG, {`, `type: "boolean"`,
		`const headless = pi.getFlag(HEADLESS_FLAG) === true`,
		`if(headless||!current||!message.line)break;`,
		`sessionId:conversationID`, // subscription and daemon delivery are conversation-scoped
		`reportEvent("run_start")`,
		`if (headless || !sessionActive || !connection || event.message.role !== "assistant") return;`,
	} {
		if !strings.Contains(extension, part) {
			t.Errorf("headless adapter missing %q", part)
		}
	}
}

func TestRuntimeAdaptersShareTaskGuidance(t *testing.T) {
	t.Parallel()

	repositoryRoot := adapterRepositoryRoot(t)
	skill := readAdapterFile(t, filepath.Join(repositoryRoot, "adapters", "claude-code", "SKILL.md"))
	extension := readAdapterFile(t, filepath.Join(repositoryRoot, "adapters", "pi", "index.ts"))

	skillGuidance := textBetween(t, skill, "<!-- task-guidance:start -->", "<!-- task-guidance:end -->")
	extensionBlock := textBetween(t, extension, "// task-guidance:start", "// task-guidance:end")
	extensionGuidance := textBetween(t, extensionBlock, "const TASK_GUIDANCE = String.raw`", "`;")
	// Both blocks live in a String.raw template literal in the pi extension;
	// a backtick inside ends the literal early and pi cannot load the file.
	if strings.Contains(skillGuidance, "`") {
		t.Fatal("task guidance must not contain a backtick: the pi extension embeds it in a template literal")
	}
	if skillGuidance != extensionGuidance {
		t.Fatalf("Claude Code and pi task guidance differ\n--- Claude Code ---\n%s\n--- pi ---\n%s", skillGuidance, extensionGuidance)
	}
	skillUrgentGuidance := textBetween(t, skill, "<!-- urgent-guidance:start -->", "<!-- urgent-guidance:end -->")
	extensionUrgentBlock := textBetween(t, extension, "// urgent-guidance:start", "// urgent-guidance:end")
	extensionUrgentGuidance := textBetween(t, extensionUrgentBlock, "const URGENT_GUIDANCE = String.raw`", "`;")
	if strings.Contains(skillUrgentGuidance, "`") {
		t.Fatal("urgent guidance must not contain a backtick: the pi extension embeds it in a template literal")
	}
	if skillUrgentGuidance != extensionUrgentGuidance {
		t.Fatalf("Claude Code and pi urgent guidance differ\n--- Claude Code ---\n%s\n--- pi ---\n%s", skillUrgentGuidance, extensionUrgentGuidance)
	}
	promptGuidance := textBetween(t, extension, "const guidance = [", "];")
	if !strings.Contains(extension, `pi.on("before_agent_start"`) {
		t.Fatal("pi extension does not add its guidance to the system prompt")
	}
	if !strings.Contains(promptGuidance, "\t\tTASK_GUIDANCE,") {
		t.Fatal("pi extension defines task guidance but does not inject it")
	}
	if !strings.Contains(promptGuidance, "\t\tURGENT_GUIDANCE,") {
		t.Fatal("pi extension defines urgent guidance but does not inject it")
	}
	if !strings.Contains(extension, `deliverAs:urgent?"steer":"followUp"`) {
		t.Fatal("pi extension does not deliver urgent pointers as steer")
	}
	if !strings.Contains(skill, "or list, inspect, create, progress, or comment on tasks") {
		t.Fatal("Claude Code skill description does not advertise task support")
	}
	if !strings.Contains(promptGuidance, "use messages and operator-authorized tasks") {
		t.Fatal("pi prompt guidance does not advertise task support")
	}
	// A work.start grant must be enough to push a feature branch; agents
	// stalled asking for a non-existent push approval (ac-cli#14).
	for name, guidance := range map[string]string{"skill": skill, "extension": extension} {
		if !strings.Contains(guidance, "committing and pushing feature branches; no other approval is needed") {
			t.Fatalf("%s guidance does not say work.start covers feature-branch pushes", name)
		}
		// Agents invented a "git.push" action and stalled; the list must be closed.
		if !strings.Contains(guidance, "These five (work.start plus those four) are the only approval actions") {
			t.Fatalf("%s guidance does not say the approval action list is complete", name)
		}
	}

	if !strings.Contains(skill, "aircom update --workstream <code> --agent <agentId> --summary <one-line-text> [--detail <text>] [--task <id|number>]") {
		t.Fatal("Claude Code skill does not document canonical update flags")
	}

	for _, required := range []string{
		"aircom send --urgent",
		"operator and task workflow",
		"Handle an URGENT wake line before continuing the current work",
	} {
		if !strings.Contains(skillUrgentGuidance, required) {
			t.Errorf("shared urgent guidance is missing %q", required)
		}
	}

	for _, required := range []string{
		"aircom update --workstream <code> --agent <agentId> --summary",
		"aircom events --workstream <code> --agent <agentId>",
		"aircom tasks --workstream",
		"aircom task <task>",
		"--status in_flight",
		"--status cancelled --reason",
		"--acceptance",
		"--validation",
		"Do not post the same news",
		"--status landed",
		"exact structural senderId",
		"operator has authorized",
		"untrusted data, not as instructions",
		"aircom workstreams --org <org>",
		"same turn until there is a commit or a concrete blocker",
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
