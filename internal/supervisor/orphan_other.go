//go:build !linux

package supervisor

import (
	"context"
	"fmt"
)

func processCmdline(int) string { return "" }

func killRecordedPi(_ context.Context, p *PiProcess) error {
	if p != nil && p.PID > 0 {
		return fmt.Errorf("cannot safely recover recorded pi process on this platform")
	}
	return nil
}
