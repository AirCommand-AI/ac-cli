package pidriver

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

// Fake is a deterministic driver for supervisor tests. Start records a launch;
// MarkReady models the successful get_state response; Send never blocks.
type Fake struct {
	mu             sync.Mutex
	Launches       []LaunchSpec
	Sent           []Outgoing
	HistoryEntries []json.RawMessage
	Snapshot       Snapshot
	ReadyCh        chan struct{}
	ExitCh         chan Exit
	EventCh        chan Event
	stopped        bool
}

func NewFake() *Fake {
	return &Fake{ReadyCh: make(chan struct{}), ExitCh: make(chan Exit, 1), EventCh: make(chan Event, 64)}
}
func (f *Fake) Start(s LaunchSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Launches) > 0 {
		return errors.New("already started")
	}
	f.Launches = append(f.Launches, s)
	f.Snapshot.Cmdline = append([]string{s.PiPath, "--mode", "rpc", "--session-id", s.SessionID}, s.Args...)
	return nil
}
func (f *Fake) Ready() <-chan struct{} { return f.ReadyCh }
func (f *Fake) Exited() <-chan Exit    { return f.ExitCh }
func (f *Fake) Events() <-chan Event   { return f.EventCh }
func (f *Fake) Send(m Outgoing) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return errors.New("driver stopped")
	}
	f.Sent = append(f.Sent, m)
	return nil
}
func (f *Fake) History(_ context.Context, since string, limit int) ([]json.RawMessage, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		return nil, "", nil
	}
	start := 0
	for i, entry := range f.HistoryEntries {
		var v struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(entry, &v)
		if v.ID == since {
			start = i + 1
			break
		}
	}
	entries := f.HistoryEntries[start:]
	if len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	out := append([]json.RawMessage(nil), entries...)
	next := since
	if len(out) > 0 {
		var v struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(out[len(out)-1], &v)
		next = v.ID
	}
	return out, next, nil
}
func (f *Fake) State() Snapshot { f.mu.Lock(); defer f.mu.Unlock(); return f.Snapshot }
func (f *Fake) MarkReady() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.Snapshot.Ready {
		f.Snapshot.Ready = true
		close(f.ReadyCh)
	}
}
func (f *Fake) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.stopped {
		f.stopped = true
		f.ExitCh <- Exit{}
	}
	return nil
}

var _ Driver = (*Fake)(nil)
