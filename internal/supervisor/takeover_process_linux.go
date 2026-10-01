//go:build linux

package supervisor

import (
	"fmt"
	"os"
	"strings"
)

func takeoverStartTime(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	idx := strings.LastIndexByte(string(data), ')')
	if idx < 0 {
		return ""
	}
	fields := strings.Fields(string(data[idx+1:]))
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}
func takeoverAlive(p *TakeoverProcess) bool {
	return p != nil && p.PID > 0 && p.StartTime != "" && takeoverStartTime(p.PID) == p.StartTime
}
