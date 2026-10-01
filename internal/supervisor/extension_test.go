package supervisor

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/adapters/pi"
	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

func TestHeadlessStaleExtensionCrashesWithoutLaunchingOrRetrying(t *testing.T) {
	m, _, _, _ := setup(t)
	path := piadapter.Path(m.Home)
	if err := os.WriteFile(path, []byte("old extension"), 0600); err != nil {
		t.Fatal(err)
	}
	launches := 0
	m.NewDriver = func(io.Writer) pidriver.Driver { launches++; return pidriver.NewFake() }
	d := definition(m.Home)
	d.Mode = "headless"
	if err := m.Start(context.Background(), d); err == nil {
		t.Fatal("stale extension accepted")
	}
	status, err := m.List(context.Background())
	if err != nil || len(status) != 1 || status[0].State != "crashed" || status[0].Reason == "" {
		t.Fatalf("status=%+v, err=%v", status, err)
	}
	if err := m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if launches != 0 {
		t.Fatalf("launched %d times", launches)
	}
	data, err := os.ReadFile(m.definitionPath(d.AgentID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "does not register --aircommand-headless") {
		t.Fatalf("reason not persisted: %s", data)
	}
}
