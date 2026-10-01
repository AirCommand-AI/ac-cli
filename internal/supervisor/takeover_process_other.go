//go:build !linux

package supervisor

import (
	"fmt"
	"os/exec"
	"strings"
)

func takeoverStartTime(pid int) string {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
func takeoverAlive(p *TakeoverProcess) bool {
	return p != nil && p.PID > 0 && p.StartTime != "" && takeoverStartTime(p.PID) == p.StartTime
}
