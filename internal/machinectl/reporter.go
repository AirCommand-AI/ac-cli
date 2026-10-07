package machinectl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

// StatusSource is the local, read-only machine snapshot plus the stopping hold.
// It never grants authority to change definitions: the response carries only
// the managed machine's state, not an agent command.
type StatusSource interface {
	List(context.Context) ([]supervisor.AgentStatus, error)
	IdleSince(context.Context) (*time.Time, error)
	AgentsStopped() bool
	SetMachineState(context.Context, string) error
}

type HTTPReporter struct {
	URL, Token, Version    string
	Client                 *http.Client
	Source                 StatusSource
	RunMode                bool // run.v1 is opt-in; managed devices keep the old check-in shape
	failureMu              sync.RWMutex
	lastError, lastErrorAt string
}

// SetCheckFailure stores a bounded, sanitized diagnostic for the next status.
// An empty value explicitly clears the run device's previous error.
func (r *HTTPReporter) SetCheckFailure(err error, at time.Time) {
	r.failureMu.Lock()
	defer r.failureMu.Unlock()
	r.lastError, r.lastErrorAt = "", ""
	if err != nil {
		r.lastError = safeMachineError(err)
		r.lastErrorAt = at.UTC().Format(time.RFC3339Nano)
	}
}

func (r *HTTPReporter) Report(ctx context.Context) error { return r.report(ctx, true) }

func (r *HTTPReporter) report(ctx context.Context, apply bool) error {
	if r.Source == nil || r.URL == "" || r.Token == "" {
		return fmt.Errorf("machine status reporter is not configured")
	}
	if ready, ok := r.Source.(interface{ WaitReady(context.Context) error }); ok {
		if err := ready.WaitReady(ctx); err != nil {
			return err
		}
	}
	agents, err := r.Source.List(ctx)
	if err != nil {
		return err
	}
	idle, err := r.Source.IdleSince(ctx)
	if err != nil {
		return err
	}
	rows := make([]struct {
		AgentID string `json:"agentId"`
		State   string `json:"state"`
		Mode    string `json:"mode"`
		Kind    string `json:"kind"`
	}, 0, len(agents))
	for _, a := range agents {
		kind := a.Kind
		if kind == "" {
			kind = "started"
		}
		rows = append(rows, struct {
			AgentID string `json:"agentId"`
			State   string `json:"state"`
			Mode    string `json:"mode"`
			Kind    string `json:"kind"`
		}{a.AgentID, a.State, a.Mode, kind})
	}
	capabilities := []string{"check-in"}
	if r.RunMode {
		capabilities = append(capabilities, "run.v1")
	}
	r.failureMu.RLock()
	lastError, lastErrorAt := r.lastError, r.lastErrorAt
	r.failureMu.RUnlock()
	body, err := json.Marshal(struct {
		Version       string     `json:"aircomVersion"`
		Capabilities  []string   `json:"capabilities"`
		IdleSince     *time.Time `json:"idleSince"`
		AgentsStopped bool       `json:"agentsStopped"`
		Agents        any        `json:"agents"`
		LastError     string     `json:"lastError"`
		LastErrorAt   string     `json:"lastErrorAt"`
	}{r.Version, capabilities, idle, r.Source.AgentsStopped(), rows, lastError, lastErrorAt})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.URL, "/")+"/agent/v1/machines/me/status", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Content-Type", "application/json")
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("machine status HTTP %d", resp.StatusCode)
	}
	var result struct {
		Machine struct {
			State string `json:"state"`
		} `json:"machine"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return err
	}
	if apply && result.Machine.State != "" {
		before := r.Source.AgentsStopped()
		if err := r.Source.SetMachineState(ctx, result.Machine.State); err != nil {
			return err
		}
		if !before && r.Source.AgentsStopped() {
			return r.report(ctx, false)
		}
	}
	return nil
}
