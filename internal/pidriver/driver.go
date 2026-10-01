// Package pidriver drives one pi RPC process per agent. Driver methods must not
// be called while holding the supervisor mutex.
package pidriver

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

// Driver is the single-owner interface for a pi RPC session.
type Driver interface {
	Start(spec LaunchSpec) error
	Ready() <-chan struct{}
	Exited() <-chan Exit
	Send(msg Outgoing) error
	Events() <-chan Event
	History(ctx context.Context, since string, limit int) (entries []json.RawMessage, next string, err error)
	State() Snapshot
	Stop(ctx context.Context) error
}

// Options configures a production driver; zero values use stderr and time.Now.
type Options struct {
	Log   io.Writer
	Clock func() time.Time
}

type LaunchSpec struct {
	PiPath, WorkDir, SessionID, ForkFrom string
	Args                                 []string
}
type Exit struct {
	Code   int
	Signal string
	Err    error
}
type Kind string

const (
	Regular   Kind = "regular"
	Urgent    Kind = "urgent"
	Interrupt Kind = "interrupt"
)

type Outgoing struct {
	Text   string
	Kind   Kind
	Source string
}

// Event retains the raw RPC event so consumers can inspect version-specific
// fields without losing information. Kind is dialog_cancelled for auto-cancelled dialogs.
type Event struct {
	Kind string
	Data map[string]any
	At   time.Time
}
type Snapshot struct {
	Ready, Streaming, Settled bool
	CurrentTool               string
	ParentToolCallID          string
	LastEvent                 time.Time
	PID, PGID                 int
	StartTime                 string
	Cmdline                   []string
}
