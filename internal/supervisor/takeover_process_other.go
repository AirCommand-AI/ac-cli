//go:build !linux

package supervisor

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

func takeoverStartTime(pid int) string {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
func takeoverAlive(p *TakeoverProcess) bool {
	return p != nil && p.PID > 0 && p.StartTime != "" && syscall.Kill(p.PID, 0) == nil && takeoverStartTime(p.PID) == p.StartTime
}
