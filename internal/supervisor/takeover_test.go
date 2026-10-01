package supervisor

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

func TestTakeoverFencesHeadlessAndResumesSameSession(t *testing.T) {
	ctx := context.Background()
	m, _, _, now := setup(t)
	d := definition(m.Home)
	d.Mode = "headless"
	var drivers []*pidriver.Fake
	m.NewDriver = func(io.Writer) pidriver.Driver { f := pidriver.NewFake(); drivers = append(drivers, f); return f }
	if err := m.Start(ctx, d); err != nil {
		t.Fatal(err)
	}
	spec, err := m.Takeover(ctx, d.Name)
	if err != nil {
		t.Fatal(err)
	}
	if spec.SessionID != d.AgentID || spec.WorkDir != d.WorkFolder || spec.PiPath != m.Pi {
		t.Fatalf("launch spec: %+v", spec)
	}
	for _, arg := range spec.Args {
		if arg == "--aircommand-headless" {
			t.Fatal("foreground pi has headless extension flag")
		}
	}
	a := m.agents[d.Name]
	if !a.takenOver || a.driver != nil || a.def.State != "taken-over" || a.lock == nil {
		t.Fatal("takeover did not fence headless session")
	}
	a.nextPoll = now.Add(time.Hour)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(drivers) != 1 {
		t.Fatal("headless relaunched during takeover")
	}
	if err := m.ResumeTakeover(d.Name); err != nil {
		t.Fatal(err)
	}
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(drivers) != 2 || drivers[1].Launches[0].SessionID != d.AgentID {
		t.Fatal("headless did not resume exact session")
	}
}
