//go:build linux || darwin

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"
)

// Terminate a foreground takeover only when its recorded process identity
// still matches and it is the leader of a separate process group. Never send
// a negative-pid signal to the CLI's or operator's shared process group.
func terminateTakeoverGroup(ctx context.Context, p *TakeoverProcess, grace time.Duration) error {
	if p == nil || p.PID <= 0 || p.StartTime == "" {
		return fmt.Errorf("takeover has no recorded process fence")
	}
	if !takeoverAlive(p) {
		return nil
	}
	group, err := syscall.Getpgid(p.PID)
	if err != nil {
		return err
	}
	if group != p.PID || !takeoverAlive(p) {
		return fmt.Errorf("takeover process group identity changed")
	}
	if err := syscall.Kill(-group, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	end := time.NewTimer(grace)
	defer end.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if errors.Is(syscall.Kill(-group, 0), syscall.ESRCH) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-end.C:
			// The group ID remains reserved while the group exists; the identity was
			// verified before SIGTERM, so SIGKILL cannot target a recycled group ID.
			if err := syscall.Kill(-group, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
			deadline := time.NewTimer(2 * time.Second)
			defer deadline.Stop()
			for {
				if errors.Is(syscall.Kill(-group, 0), syscall.ESRCH) {
					return nil
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-ticker.C:
				case <-deadline.C:
					return fmt.Errorf("foreground process group %d did not exit", group)
				}
			}
		}
	}
}
