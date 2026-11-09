package claudecodeadapter

import (
	"strings"
	"testing"
)

func TestHeadlessGuidanceCarriesSkillBlocks(t *testing.T) {
	g := HeadlessGuidance("/usr/local/bin/aircom")
	for _, want := range []string{"/usr/local/bin/aircom inbox", "kind is role", "team-watch", block("<!-- task-guidance:start -->", "<!-- task-guidance:end -->"), block("<!-- urgent-guidance:start -->", "<!-- urgent-guidance:end -->")} {
		if want == "" || !strings.Contains(g, want) {
			t.Fatalf("guidance missing %q", want)
		}
	}
	if strings.Contains(g, "join --listen") {
		t.Fatal("headless guidance must not tell the agent to listen")
	}
}
