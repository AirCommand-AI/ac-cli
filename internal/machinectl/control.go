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
	Reconciler  Reconciler
	Reporter    Reporter
	ReportFirst bool // run machines must become running before cloning private repos
	checks      chan struct{}
	interval    time.Duration
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
	var lastError error
	var lastErrorAt time.Time
	var reportFailure bool
	setReporterFailure := func() {
		if reporter, ok := c.Reporter.(interface{ SetCheckFailure(error, time.Time) }); ok {
			reporter.SetCheckFailure(lastError, lastErrorAt)
		}
	}
	check := func() {
		if c.ReportFirst && c.Reporter != nil && ctx.Err() == nil {
			setReporterFailure()
			if err := c.Reporter.Report(ctx); err != nil {
				if ctx.Err() == nil {
					lastError, lastErrorAt, reportFailure = err, time.Now().UTC(), true
					if onError != nil {
						onError(err)
					}
				}
				return
			}
			if reportFailure {
				lastError = nil
				reportFailure = false
			}
		}
		var reconcileErr error
		if c.Reconciler != nil {
			reconcileErr = c.Reconciler.Reconcile(ctx)
		}
		if reconcileErr != nil && ctx.Err() == nil {
			lastError, lastErrorAt, reportFailure = reconcileErr, time.Now().UTC(), false
			if onError != nil {
				onError(reconcileErr)
			}
		} else if !reportFailure {
			lastError = nil
		}
		if c.Reporter != nil && ctx.Err() == nil {
			setReporterFailure()
			if err := c.Reporter.Report(ctx); err != nil && ctx.Err() == nil {
				lastError, lastErrorAt, reportFailure = err, time.Now().UTC(), true
				if onError != nil {
					onError(err)
				}
			} else if reconcileErr == nil && reportFailure {
				lastError = nil
				reportFailure = false
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
