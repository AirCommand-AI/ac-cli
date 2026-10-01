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

// killRecordedPi fences reuse of a PID by checking kernel start ticks, the
// executable and the original process group before signaling the group.
func killRecordedPi(ctx context.Context, p *PiProcess) error {
	if p == nil || p.PID <= 0 || p.PGID <= 0 || p.StartTime == "" || p.Cmdline == "" {
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
	cmd, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", p.PID))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimRight(string(cmd), "\x00") != p.Cmdline {
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
			_ = syscall.Kill(-p.PGID, syscall.SIGKILL)
			return fmt.Errorf("timed out waiting for orphan pi pid %d", p.PID)
		case <-tick.C:
		}
	}
}
