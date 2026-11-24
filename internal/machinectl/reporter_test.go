package machinectl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

type fakeStatusSource struct{}

func (s *fakeStatusSource) List(context.Context) ([]supervisor.AgentStatus, error) {
	return []supervisor.AgentStatus{{AgentID: "agm_1", Mode: "headless", State: "running"}}, nil
}

func TestHTTPReporterCapabilities(t *testing.T) {
	cases := []struct {
		name    string
		runMode bool
		want    []string
	}{
		{"laptop", false, []string{"check-in", "agent-status-v2", "add-repos"}},
		{"run machine", true, []string{"check-in", "agent-status-v2", "add-repos", "run.v1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Capabilities []string `json:"capabilities"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				got = body.Capabilities
				_, _ = w.Write([]byte(`{"machine":{"state":"online"}}`))
			}))
			defer server.Close()
			if err := (&HTTPReporter{URL: server.URL, Token: "machine", Source: &fakeStatusSource{}, RunMode: tc.runMode}).Report(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("capabilities=%v want %v", got, tc.want)
			}
			if !slices.Contains(got, CapabilityAddRepos) || CapabilityAddRepos != "add-repos" {
				t.Fatalf("add-repos capability missing: %v", got)
			}
		})
	}
}

func TestHTTPReporterSendsContractAndIgnoresMachineState(t *testing.T) {
	source := &fakeStatusSource{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/agent/v1/machines/me/status" || r.Header.Get("Authorization") != "Bearer machine-token" {
			t.Errorf("route %+v", r)
		}
		var body struct {
			Version      string   `json:"aircomVersion"`
			Capabilities []string `json:"capabilities"`
			Agents       []struct {
				AgentID string `json:"agentId"`
				State   string `json:"state"`
				Mode    string `json:"mode"`
				Kind    string `json:"kind"`
			} `json:"agents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Version != "v0.19.0" || len(body.Capabilities) != 3 || body.Capabilities[0] != "check-in" || body.Capabilities[1] != "agent-status-v2" || body.Capabilities[2] != "add-repos" || len(body.Agents) != 1 || body.Agents[0].AgentID != "agm_1" || body.Agents[0].Mode != "headless" || body.Agents[0].Kind != "started" {
			t.Errorf("body %+v", body)
		}
		_, _ = w.Write([]byte(`{"machine":{"state":"stopping","stateAt":"2026-10-02T00:00:00Z"}}`))
	}))
	defer server.Close()
	reporter := &HTTPReporter{URL: server.URL, Token: "machine-token", Version: "v0.19.0", Source: source}
	if err := reporter.Report(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("a stopping reply must not trigger a second report: calls %d", calls)
	}
}

// TestHTTPReporterReadsTheCurrentToken uses a renewed machine token without a
// daemon restart.
func TestHTTPReporterReadsTheCurrentToken(t *testing.T) {
	var used []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		used = append(used, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"machine":{"state":"online"}}`))
	}))
	defer server.Close()
	current := "first"
	reporter := &HTTPReporter{URL: server.URL, Token: "stale", TokenLoader: func() (string, error) { return current, nil }, Source: &fakeStatusSource{}}
	for _, token := range []string{"first", "renewed"} {
		current = token
		if err := reporter.Report(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(used) != 2 || used[0] != "Bearer first" || used[1] != "Bearer renewed" {
		t.Fatalf("tokens used %v", used)
	}
}
