package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A model turn must not synchronously probe a repo or its remote. Git remote
// belongs to full reports only; the per-turn path checks just the branch.
func TestPiRuntimeBranchCheckIsAsyncAndBranchOnly(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "adapters", "pi", "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "const checkBranch =")
	end := strings.Index(source, "const reportState =")
	if start < 0 || end < start {
		t.Fatal("missing per-turn branch check")
	}
	check := source[start:end]
	if !strings.Contains(check, `execFile("git", ["branch", "--show-current"]`) {
		t.Fatal("per-turn git branch check is not asynchronous")
	}
	if strings.Contains(check, "execFileSync") || strings.Contains(check, "gitInfo(") || strings.Contains(check, "remote") {
		t.Fatal("per-turn path probes more than the branch")
	}
	if !strings.Contains(source, `pi.on("turn_start", async (_event, ctx) => { checkBranch(ctx); reportState("working"); })`) {
		t.Fatal("turn_start must only schedule the asynchronous branch check and the throttled working refresh")
	}
	if !strings.Contains(source, `if (lastState === state && now - lastStateAt < 60_000) return;`) || !strings.Contains(source, `execFile(cliPath, ["state"`) {
		t.Fatal("the per-turn working refresh must stay throttled and asynchronous")
	}
	if !strings.Contains(source, "machine: registeredMachineName(), hostname: hostname()") {
		t.Fatal("machine name must come from registration; hostname is separate")
	}
}
