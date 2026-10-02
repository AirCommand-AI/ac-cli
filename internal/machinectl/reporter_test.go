package machinectl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

type fakeStatusSource struct {
	state   string
	stopped bool
	idle    time.Time
}

func (s *fakeStatusSource) List(context.Context) ([]supervisor.AgentStatus, error) {
	return []supervisor.AgentStatus{{AgentID: "agm_1", Mode: "headless", State: "running"}}, nil
}
func (s *fakeStatusSource) IdleSince(context.Context) (*time.Time, error) { return &s.idle, nil }
func (s *fakeStatusSource) AgentsStopped() bool                           { return s.stopped }
func (s *fakeStatusSource) SetMachineState(_ context.Context, state string) error {
	s.state = state
	return nil
}

func TestHTTPReporterSendsContractAndAppliesMachineState(t *testing.T) {
	source := &fakeStatusSource{idle: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/agent/v1/machines/me/status" || r.Header.Get("Authorization") != "Bearer machine-token" {
			t.Errorf("route %+v", r)
		}
		var body struct {
			Version       string     `json:"aircomVersion"`
			Capabilities  []string   `json:"capabilities"`
			IdleSince     *time.Time `json:"idleSince"`
			AgentsStopped bool       `json:"agentsStopped"`
			Agents        []struct {
				AgentID string `json:"agentId"`
				State   string `json:"state"`
				Mode    string `json:"mode"`
			} `json:"agents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Version != "v0.19.0" || len(body.Capabilities) != 1 || body.Capabilities[0] != "check-in" || body.IdleSince == nil || !body.IdleSince.Equal(source.idle) || body.AgentsStopped != source.stopped || len(body.Agents) != 1 || body.Agents[0].AgentID != "agm_1" || body.Agents[0].Mode != "headless" {
			t.Errorf("body %+v", body)
		}
		_, _ = w.Write([]byte(`{"machine":{"state":"stopping","stateAt":"2026-10-02T00:00:00Z"}}`))
	}))
	defer server.Close()
	reporter := &HTTPReporter{URL: server.URL, Token: "machine-token", Version: "v0.19.0", Source: source}
	if err := reporter.Report(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || source.state != "stopping" {
		t.Fatalf("calls %d state %q", calls, source.state)
	}
}
