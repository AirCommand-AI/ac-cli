//go:build linux

package peercred

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func PeerPID(conn net.Conn) (int, error) {
	sc, ok := conn.(interface {
		SyscallConn() (syscall.RawConn, error)
	})
	if !ok {
		return 0, fmt.Errorf("peer credentials require a Unix socket")
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return 0, err
	}
	pid := 0
	var readErr error
	if err := raw.Control(func(fd uintptr) {
		var cred *unix.Ucred
		cred, readErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if readErr == nil {
			pid = int(cred.Pid)
		}
	}); err != nil {
		return 0, err
	}
	if readErr != nil {
		return 0, readErr
	}
	if pid <= 0 {
		return 0, fmt.Errorf("invalid peer pid")
	}
	return pid, nil
}

func parent(pid int) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	index := strings.LastIndexByte(string(data), ')')
	if index < 0 {
		return 0, fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(data[index+1:]))
	if len(fields) < 2 {
		return 0, fmt.Errorf("invalid process stat")
	}
	return strconv.Atoi(fields[1])
}

// Descendant includes the session process itself and walks only the kernel
// parent chain. A forged sessionPid cannot authorize a different process.
func Descendant(peerPID, sessionPID int) bool {
	if peerPID <= 0 || sessionPID <= 0 {
		return false
	}
	seen := map[int]bool{}
	for pid := peerPID; pid > 1 && !seen[pid]; {
		// Mark before moving up: marking in the loop's post statement would mark
		// the parent before it is checked and stop after the first step.
		seen[pid] = true
		if pid == sessionPID {
			return true
		}
		var err error
		pid, err = parent(pid)
		if err != nil {
			return false
		}
	}
	return sessionPID == 1 && peerPID == 1
}
