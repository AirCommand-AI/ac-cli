// Package agentlock gives one agent at most one live holder on a machine.
//
// Two sessions sharing an agent is not untidy, it is lossy: they share the
// saved poll cursor, so whichever polls first consumes a notification and
// advances past it while the other never learns the message existed, and
// either can acknowledge work the other is still doing.
//
// The lock is an advisory whole-file lock held by the running process. The
// kernel releases it when that process exits, so a crashed session leaves
// nothing stale to reclaim.
package agentlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

// ErrHeld reports that another live process already holds this agent.
var ErrHeld = errors.New("another process on this machine is already using this agent")

// Lock is a held agent lock. Release it when the holder stops.
type Lock struct {
	file *os.File
}

// Path is the lock file for an agent. It lives beside that agent's
// credential so it inherits the same owner-only directory.
func Path(home string, agentID string) string {
	return filepath.Join(storagepath.AgentDirectory(home, agentID), "lock")
}

// Acquire takes the agent lock without blocking. It returns ErrHeld when a
// live process already holds it.
func Acquire(home string, agentID string) (*Lock, error) {
	directory, err := storagepath.EnsureAgentDirectory(home, agentID)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(directory, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open agent lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, fmt.Errorf("lock agent: %w", err)
	}
	return &Lock{file: file}, nil
}

// Release drops the lock. The kernel would drop it on exit anyway; this makes
// the release explicit for long-lived processes that keep running.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	err := l.file.Close()
	l.file = nil
	return err
}

// Held reports whether a live process currently holds this agent. It answers
// by trying to take the lock and immediately dropping it again, so it can
// never report a stale holder. A machine that cannot open the file at all is
// reported as unheld, because refusing to reuse an agent over an unreadable
// lock would be worse than the race it prevents.
func Held(home string, agentID string) bool {
	lock, err := Acquire(home, agentID)
	if err != nil {
		return errors.Is(err, ErrHeld)
	}
	_ = lock.Release()
	return false
}
