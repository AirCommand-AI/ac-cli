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
	for _, part := range []string{`pi.on("agent_start", async () => { reportState("working"); })`, `pi.on("agent_end", async () => { reportState("idle"); })`} {
		if !strings.Contains(text, part) {
			t.Fatalf("missing pi run boundary handler: %s", part)
		}
	}
	if strings.Contains(text, `pi.on("turn_start", async () => { reportState`) || strings.Contains(text, `pi.on("turn_end", async () => { reportState`) {
		t.Fatal("state is still reported per model turn")
	}
}
