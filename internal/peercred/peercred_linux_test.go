//go:build linux

package peercred

import (
	"os"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/agentlock"
)

func TestSelfDescendantAndLockOwner(t *testing.T) {
	if !Descendant(os.Getpid(), os.Getpid()) {
		t.Fatal("self PID not descendant")
	}
	if Descendant(os.Getpid(), 99999999) {
		t.Fatal("invented ancestor accepted")
	}
	home := t.TempDir()
	lock, err := agentlock.Acquire(home, "agm_one")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if got := LockHolder(agentlock.Path(home, "agm_one")); got != os.Getpid() {
		t.Fatalf("lock holder = %d, want %d", got, os.Getpid())
	}
}
