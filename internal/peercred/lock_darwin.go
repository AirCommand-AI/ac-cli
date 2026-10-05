//go:build darwin

package peercred

import (
	"os/exec"
	"strconv"
	"strings"
)

func LockHolder(path string) int {
	out, err := exec.Command("lsof", "-t", path).Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Fields(string(out)) {
		pid, e := strconv.Atoi(line)
		if e == nil && pid > 0 {
			return pid
		}
	}
	return 0
}
