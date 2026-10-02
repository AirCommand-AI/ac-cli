package supervisor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLocalControlReturnsBusyDuringSlowReconcile(t *testing.T) {
	m, _, _, _ := setup(t)
	m.OperationGate = &sync.Mutex{}
	m.OperationGate.Lock()
	defer m.OperationGate.Unlock()
	start := time.Now()
	_, err := m.LockLocalChange()
	if !errors.Is(err, ErrControlBusy) || time.Since(start) > time.Second {
		t.Fatalf("busy not immediate: %v", err)
	}
}

type httpStatusFailure int

func (e httpStatusFailure) Error() string   { return fmt.Sprintf("HTTP %d", e) }
func (e httpStatusFailure) HTTPStatus() int { return int(e) }

func TestRemoveClearsPendingDesired(t *testing.T) {
	m, _, _, _ := setup(t)
	d := definition(m.Home)
	if err := m.Start(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	calls := 0
	m.DesiredPost = func(context.Context, AgentDefinition) (int64, error) { calls++; return 0, errors.New("offline") }
	if err := m.PostDesired(context.Background(), d.Name); err != nil {
		t.Fatal(err)
	}
	if len(m.pendingDesired) != 1 {
		t.Fatal("missing retry")
	}
	if err := m.Remove(context.Background(), d.Name); err != nil {
		t.Fatal(err)
	}
	if skip := m.FlushDesired(context.Background()); len(skip) != 0 || len(m.pendingDesired) != 0 || calls != 1 {
		t.Fatalf("removed pending retried: skip=%v pending=%v calls=%d", skip, m.pendingDesired, calls)
	}
}

func TestFlushDesiredDropsPermanentAndSkipsOnlyTransient(t *testing.T) {
	for _, status := range []int{400, 404, 409} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			m, _, _, _ := setup(t)
			first := definition(m.Home)
			second := first
			second.Name = "eng-2"
			second.AgentID = "agm_2"
			for _, d := range []AgentDefinition{first, second} {
				if err := m.Start(context.Background(), d); err != nil {
					t.Fatal(err)
				}
			}
			calls := map[string]int{}
			m.DesiredPost = func(_ context.Context, d AgentDefinition) (int64, error) {
				calls[d.Name]++
				if calls[d.Name] == 1 {
					return 0, errors.New("temporary")
				}
				if d.Name == first.Name {
					return 0, httpStatusFailure(status)
				}
				return 8, nil
			}
			for _, d := range []AgentDefinition{first, second} {
				if err := m.PostDesired(context.Background(), d.Name); err != nil {
					t.Fatal(err)
				}
			}
			if skip := m.FlushDesired(context.Background()); len(skip) != 0 {
				t.Fatalf("skipped healthy agent: %v", skip)
			}
			defs := m.Definitions()
			if defs[0].Revision == 0 && defs[1].Revision == 0 {
				t.Fatal("healthy agent revision not advanced")
			}
			if len(m.pendingDesired) != 0 {
				t.Fatalf("permanent pending retained: %v", m.pendingDesired)
			}
			if calls[first.Name] != 2 || calls[second.Name] != 2 {
				t.Fatalf("unexpected calls: %v", calls)
			}
		})
	}
}

func TestFlushDesiredTransientSkipsOnlyAffectedAgent(t *testing.T) {
	m, _, _, _ := setup(t)
	first := definition(m.Home)
	second := first
	second.Name = "eng-2"
	second.AgentID = "agm_2"
	for _, d := range []AgentDefinition{first, second} {
		if err := m.Start(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
	calls := map[string]int{}
	m.DesiredPost = func(_ context.Context, d AgentDefinition) (int64, error) {
		calls[d.Name]++
		if calls[d.Name] == 1 || d.Name == first.Name {
			return 0, errors.New("offline")
		}
		return 5, nil
	}
	for _, d := range []AgentDefinition{first, second} {
		if err := m.PostDesired(context.Background(), d.Name); err != nil {
			t.Fatal(err)
		}
	}
	skip := m.FlushDesired(context.Background())
	if !skip[first.AgentID] || skip[second.AgentID] || len(skip) != 1 {
		t.Fatalf("skip=%v", skip)
	}
	if calls[first.Name] != 2 || calls[second.Name] != 2 {
		t.Fatalf("other post not attempted: %v", calls)
	}
}
