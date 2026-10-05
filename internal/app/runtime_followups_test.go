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
	end := strings.Index(source, "const reportEvent=")
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
	if !strings.Contains(source, `pi.on("turn_start", async (_event, ctx) => { checkBranch(ctx); reportEvent("turn"); })`) {
		t.Fatal("turn_start must schedule the branch check and a daemon event")
	}
	if strings.Contains(source, `execFile(cliPath, ["state"`) {
		t.Fatal("pi must not report state directly")
	}
	if !strings.Contains(source, "machine: registeredMachineName(), hostname: hostname()") {
		t.Fatal("machine name must come from registration; hostname is separate")
	}
}
