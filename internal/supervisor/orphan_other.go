//go:build !linux

package supervisor

import (
	"context"
	"fmt"
	"syscall"
)

func killRecordedPi(_ context.Context, p *PiProcess) error {
	if p != nil && p.PID > 0 {
		if err := syscall.Kill(p.PID, 0); err == syscall.ESRCH {
			return nil
		}
		return fmt.Errorf("cannot safely recover recorded pi process on this platform")
	}
	return nil
}
