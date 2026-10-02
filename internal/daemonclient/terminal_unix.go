//go:build linux || darwin

package daemonclient

import (
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

// foregroundTTY starts pi in its own process group and hands it terminal
// foreground ownership. The CLI restores its own group after pi exits.
func foregroundTTY(input *os.File) (int, bool) {
	var group int32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, input.Fd(), uintptr(syscall.TIOCGPGRP), uintptr(unsafe.Pointer(&group)))
	return int(input.Fd()), errno == 0
}
func restoreForeground(fd int) {
	// tcsetpgrp from the CLI's background group would otherwise stop the CLI.
	signal.Ignore(syscall.SIGTTOU)
	defer signal.Reset(syscall.SIGTTOU)
	group := int32(syscall.Getpgrp())
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(syscall.TIOCSPGRP), uintptr(unsafe.Pointer(&group)))
}
