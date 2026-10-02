// Package machinectl schedules server-authoritative agent reconciliation and
// machine status reporting. It owns no supervisor lock or inbound listener.
package machinectl

import (
	"context"
	"time"
)

// Reconciler fetches and applies agent definitions outside the supervisor lock.
type Reconciler interface {
	Reconcile(context.Context) error
}

// Reporter sends the machine's state to the server.
type Reporter interface {
	Report(context.Context) error
}

// Control coalesces content-free check-in signals with periodic checks.
// Construct with New; a check-in never carries a definition or command.
type Control struct {
	Reconciler Reconciler
	Reporter   Reporter
	checks     chan struct{}
	interval   time.Duration
}

func New(reconciler Reconciler, reporter Reporter) *Control {
	return &Control{Reconciler: reconciler, Reporter: reporter, checks: make(chan struct{}, 1), interval: 30 * time.Second}
}

// CheckIn is non-blocking, including while a check is already in progress.
func (c *Control) CheckIn() {
	select {
	case c.checks <- struct{}{}:
	default:
	}
}

// Run checks once at startup, then on a check-in or every 30 seconds. A
// failed check is retried on the next signal/tick; callers supply an error
// handler rather than letting transient server failures stop the daemon.
func (c *Control) Run(ctx context.Context, onError func(error)) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	check := func() {
		if c.Reconciler != nil {
			if err := c.Reconciler.Reconcile(ctx); err != nil && onError != nil && ctx.Err() == nil {
				onError(err)
			}
		}
		if c.Reporter != nil && ctx.Err() == nil {
			if err := c.Reporter.Report(ctx); err != nil && onError != nil && ctx.Err() == nil {
				onError(err)
			}
		}
	}
	check()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		case <-c.checks:
			check()
		}
	}
}
