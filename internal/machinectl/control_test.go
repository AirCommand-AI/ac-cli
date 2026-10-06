package machinectl

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type reconcileFunc func(context.Context) error

func (f reconcileFunc) Reconcile(ctx context.Context) error { return f(ctx) }

type reportFunc func(context.Context) error

func (f reportFunc) Report(ctx context.Context) error { return f(ctx) }

func TestRunModeReportsBeforeCloning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls []string
	c := New(reconcileFunc(func(context.Context) error { calls = append(calls, "clone"); cancel(); return nil }), reportFunc(func(context.Context) error { calls = append(calls, "report"); return nil }))
	c.ReportFirst = true
	c.Run(ctx, nil)
	if len(calls) != 2 || calls[0] != "report" || calls[1] != "clone" {
		t.Fatalf("startup order %v", calls)
	}
}
func TestCheckInCoalescesAndChecksOnStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reconciles, reports atomic.Int32
	seen := make(chan struct{}, 3)
	c := New(reconcileFunc(func(context.Context) error { reconciles.Add(1); return errors.New("retry") }), reportFunc(func(context.Context) error { reports.Add(1); seen <- struct{}{}; return nil }))
	var failures atomic.Int32
	done := make(chan struct{})
	c.CheckIn()
	c.CheckIn() // both signals queue only one check while startup is pending
	go func() { c.Run(ctx, func(error) { failures.Add(1) }); close(done) }()
	wait := func() {
		t.Helper()
		select {
		case <-seen:
		case <-time.After(time.Second):
			t.Fatal("check timed out")
		}
	}
	wait()
	wait()
	if reconciles.Load() != 2 || reports.Load() != 2 || failures.Load() != 2 {
		t.Fatalf("reconcile=%d report=%d errors=%d", reconciles.Load(), reports.Load(), failures.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop failed to stop")
	}
}
