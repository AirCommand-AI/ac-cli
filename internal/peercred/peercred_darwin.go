//go:build darwin

package peercred

import (
	"fmt"
	"net"
	"os/exec"
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
	if err := raw.Control(func(fd uintptr) { pid, readErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID) }); err != nil {
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

func Descendant(peerPID, sessionPID int) bool {
	if peerPID <= 0 || sessionPID <= 0 {
		return false
	}
	seen := map[int]bool{}
	for pid := peerPID; pid > 1 && !seen[pid]; seen[pid] = true {
		if pid == sessionPID {
			return true
		}
		out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			return false
		}
	}
	return sessionPID == 1 && peerPID == 1
}
