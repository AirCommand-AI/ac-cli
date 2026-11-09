package machinectl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

// StatusSource is the local, read-only machine snapshot. The status response
// never changes local state.
type StatusSource interface {
	List(context.Context) ([]supervisor.AgentStatus, error)
}

type HTTPReporter struct {
	URL, Token, Version string
	// TokenLoader, when set, supplies the machine token on every report
	// instead of Token, so a renewed credential is used without a restart.
	TokenLoader func() (string, error)
	Client      *http.Client
	Source      StatusSource
	RunMode     bool // run.v1 is opt-in
	// Metrics, when set, adds a machine health sample to every report.
	Metrics                interface{ Collect() Metrics }
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

func (r *HTTPReporter) Report(ctx context.Context) error { return r.report(ctx) }

func (r *HTTPReporter) token() (string, error) {
	if r.TokenLoader != nil {
		return r.TokenLoader()
	}
	return r.Token, nil
}

func (r *HTTPReporter) report(ctx context.Context) error {
	token, err := r.token()
	if r.Source == nil || r.URL == "" || err != nil || token == "" {
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
	var metrics *Metrics
	if r.Metrics != nil {
		sample := r.Metrics.Collect()
		metrics = &sample
	}
	body, err := json.Marshal(struct {
		Version      string   `json:"aircomVersion"`
		Capabilities []string `json:"capabilities"`
		Agents       any      `json:"agents"`
		LastError    string   `json:"lastError"`
		LastErrorAt  string   `json:"lastErrorAt"`
		Metrics      *Metrics `json:"metrics,omitempty"`
	}{r.Version, capabilities, rows, lastError, lastErrorAt, metrics})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.URL, "/")+"/agent/v1/machines/me/status", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
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
	return nil
}
