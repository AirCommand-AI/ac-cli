package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A pi run may have many model/tool turns. Presence transitions belong to the
// entire run, not its individual turns.
func TestPiPresenceReportsRunBoundariesNotTurnBoundaries(t *testing.T) {
	path := filepath.Join("..", "..", "adapters", "pi", "index.ts")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, part := range []string{`pi.on("agent_start", async () => { reportEvent("run_start"); })`, `pi.on("agent_end", async () => { reportEvent("run_end"); })`} {
		if !strings.Contains(text, part) {
			t.Fatalf("missing pi run boundary handler: %s", part)
		}
	}
	if strings.Contains(text, `execFile(cliPath, ["state"`) {
		t.Fatal("pi still reports state directly to server")
	}
	if !strings.Contains(text, `reportEvent("tool_end")`) {
		t.Fatal("missing tool completion activity")
	}
}
