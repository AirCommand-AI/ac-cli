package daemon

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/machinectl"
)

type countReconcile struct{ calls atomic.Int32 }

func (c *countReconcile) Reconcile(context.Context) error { c.calls.Add(1); return nil }

type countReport struct {
	calls atomic.Int32
	done  chan struct{}
}

func (c *countReport) Report(context.Context) error { c.calls.Add(1); c.done <- struct{}{}; return nil }

func TestCheckInTriggersSingleCombinedReconcileAndStatus(t *testing.T) {
	reconcile := &countReconcile{}
	report := &countReport{done: make(chan struct{}, 3)}
	socket := &SocketClient{Control: machinectl.New(nil, report)}
	combined := machinectl.New(reconcile, nil)
	bindMachineControl(socket, combined)
	if socket.Control != combined || socket.OnCheckIn == nil || socket.OnConnect == nil {
		t.Fatal("control loops were not joined")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() { combined.Run(ctx, nil); close(finished) }()
	wait := func() {
		t.Helper()
		select {
		case <-report.done:
		case <-time.After(time.Second):
			t.Fatal("status was not reported")
		}
	}
	wait()
	socket.OnCheckIn()
	wait()
	if reconcile.calls.Load() != 2 || report.calls.Load() != 2 {
		t.Fatalf("reconcile %d report %d", reconcile.calls.Load(), report.calls.Load())
	}
	cancel()
	<-finished
}
