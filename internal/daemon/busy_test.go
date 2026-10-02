package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

type busySupervisor struct{ fakeSupervisor }

func (*busySupervisor) LockLocalChange() (func(), error) { return nil, supervisor.ErrControlBusy }

func TestBusyReconcileRejectsLocalControlImmediately(t *testing.T) {
	for _, op := range []string{"agent.start", "agent.stop", "agent.mode", "agent.remove"} {
		s := &busySupervisor{}
		start := time.Now()
		response := dispatch(context.Background(), s, Request{Op: op, Name: "eng-1", AgentID: "agm_1", Organization: "org_a", Workstream: "348", WorkFolder: "/tmp/work", Mode: "headless"}, time.Time{}, "dev", "", 0, nil)
		if response.Error == nil || response.Error.Code != "busy" || time.Since(start) > time.Second {
			t.Fatalf("%s: response=%+v", op, response)
		}
	}
}
