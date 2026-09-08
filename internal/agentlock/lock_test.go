package agentlock

import (
	"os"
	"testing"
)

func TestSecondAcquireIsRefusedWhileTheFirstIsHeld(t *testing.T) {
	home := t.TempDir()

	first, err := Acquire(home, "agm_test")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if !Held(home, "agm_test") {
		t.Fatal("Held reported false while the lock was held")
	}
	if _, err := Acquire(home, "agm_test"); err != ErrHeld {
		t.Fatalf("second Acquire = %v, want ErrHeld", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if Held(home, "agm_test") {
		t.Fatal("Held reported true after release")
	}
	second, err := Acquire(home, "agm_test")
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	_ = second.Release()
}

func TestLocksAreIndependentPerAgent(t *testing.T) {
	home := t.TempDir()

	one, err := Acquire(home, "agm_one")
	if err != nil {
		t.Fatalf("Acquire agm_one: %v", err)
	}
	defer func() { _ = one.Release() }()

	two, err := Acquire(home, "agm_two")
	if err != nil {
		t.Fatalf("Acquire agm_two while agm_one is held: %v", err)
	}
	_ = two.Release()
}

func TestReleasingTwiceAndReleasingNilAreHarmless(t *testing.T) {
	home := t.TempDir()
	lock, err := Acquire(home, "agm_test")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	var absent *Lock
	if err := absent.Release(); err != nil {
		t.Fatalf("Release on nil: %v", err)
	}
}

func TestLockFileIsOwnerOnly(t *testing.T) {
	home := t.TempDir()
	lock, err := Acquire(home, "agm_test")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = lock.Release() }()

	info, err := os.Stat(Path(home, "agm_test"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("lock file mode = %o, want 600", mode)
	}
}
