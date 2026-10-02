package supervisor

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestStartPreservesAppliedRevisionAfterStop(t *testing.T) {
	m, _, _, _ := setup(t)
	d := definition(m.Home)
	if err := m.Start(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if err := m.MarkRevision(d.Name, 5); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(context.Background(), d.Name); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	got := m.Definitions()
	if len(got) != 1 || got[0].Revision != 5 {
		t.Fatalf("lost revision after restart: %+v", got)
	}
}

func TestPostDesiredFailureReturnsSuccessThenRetries(t *testing.T) {
	m, _, _, _ := setup(t)
	m.OperationGate = &sync.Mutex{}
	d := definition(m.Home)
	if err := m.Start(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	m.DesiredPost = func(_ context.Context, current AgentDefinition) (int64, error) {
		attempts++
		if attempts == 1 {
			return 0, errors.New("offline")
		}
		if current.Desired != "stopped" {
			t.Fatalf("unexpected desired: %+v", current)
		}
		return 4, nil
	}
	unlock := m.LockLocalChange()
	if err := m.Stop(context.Background(), d.Name); err != nil {
		t.Fatal(err)
	}
	if err := m.PostDesired(context.Background(), d.Name); err != nil {
		t.Fatalf("successful local stop reported failed: %v", err)
	}
	unlock()
	if err := m.FlushDesired(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || m.Definitions()[0].Revision != 4 {
		t.Fatalf("retry attempts=%d state=%+v", attempts, m.Definitions())
	}
}
