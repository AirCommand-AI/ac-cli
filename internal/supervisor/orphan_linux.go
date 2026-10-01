//go:build linux

package supervisor

import (
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
)

// killRecordedPi fences PID reuse with kernel start ticks and the original
// process group. pi changes process.title, so command line is not a stable fence.
func killRecordedPi(ctx context.Context, p *PiProcess) error {
	if p == nil || p.PID <= 0 || p.PGID <= 0 || p.StartTime == "" {
		return nil
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p.PID))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	fields := strings.Fields(string(stat)[strings.LastIndex(string(stat), ")")+1:])
	if len(fields) < 20 || fields[19] != p.StartTime {
		return nil
	}
	pgid, err := syscall.Getpgid(p.PID)
	if err == syscall.ESRCH {
		return nil
	}
	if err != nil {
		return err
	}
	if pgid != p.PGID || p.PGID != p.PID {
		return nil
	}
	if err := syscall.Kill(-p.PGID, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		current, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p.PID))
		if os.IsNotExist(err) || err == nil && strings.HasPrefix(string(current)[strings.LastIndex(string(current), ")")+1:], " Z ") {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if err := syscall.Kill(-p.PGID, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
				return err
			}
			wait := time.NewTimer(2 * time.Second)
			defer wait.Stop()
			for {
				current, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p.PID))
				if os.IsNotExist(err) || err == nil && strings.HasPrefix(string(current)[strings.LastIndex(string(current), ")")+1:], " Z ") {
					return nil
				}
				if err != nil {
					return err
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-wait.C:
					return fmt.Errorf("orphan pi pid %d survived SIGKILL", p.PID)
				case <-tick.C:
				}
			}
		case <-tick.C:
		}
	}
}
