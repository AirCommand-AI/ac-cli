//go:build linux

package peercred

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// LockHolder reports the kernel's lock owner when an old listener holds the
// agent's flock. It never guesses from a stale pid file.
func LockHolder(path string) int {
	stat, err := os.Stat(path)
	if err != nil {
		return 0
	}
	id, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	data, err := os.ReadFile("/proc/locks")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 6 || f[1] != "FLOCK" {
			continue
		}
		parts := strings.Split(f[5], ":")
		if len(parts) != 3 {
			continue
		}
		major, e1 := strconv.ParseUint(parts[0], 16, 32)
		minor, e2 := strconv.ParseUint(parts[1], 16, 32)
		inode, e3 := strconv.ParseUint(parts[2], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || major != uint64(unix.Major(uint64(id.Dev))) || minor != uint64(unix.Minor(uint64(id.Dev))) || inode != id.Ino {
			continue
		}
		pid, e := strconv.Atoi(f[4])
		if e == nil && pid > 0 {
			return pid
		}
	}
	return 0
}
