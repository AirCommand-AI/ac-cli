package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMachineMetrics(t *testing.T) {
	credential := testCredential()
	active := `{"runs":[{"runId":"` + testMachineRunID + `"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/v1/workstreams/694/machine-runs":
			if r.URL.Query().Get("state") != "active" {
				t.Errorf("active lookup query=%s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(active))
		case "/agent/v1/workstreams/694/machine-runs/" + testMachineRunID + "/metrics":
			if r.URL.Query().Get("cursor") == "bad" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":"Invalid cursor","code":"BadRequest"}`))
				return
			}
			_, _ = w.Write([]byte(`{"points":[{"at":"2026-11-07T19:26:00Z","sample":{"disk":[{"mount":"/","availBytes":0}]}}],"q":"` + r.URL.RawQuery + `"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	tests := []struct {
		name     string
		active   string
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{name: "explicit run and window", args: []string{"--run", testMachineRunID, "--since", "2026-11-07T18:00:00Z", "--until", "2026-11-07T20:00:00Z", "--limit", "60"}, wantOut: `limit=60&since=2026-11-07T18%3A00%3A00Z&until=2026-11-07T20%3A00%3A00Z`},
		{name: "uses the one running machine", wantOut: `"availBytes":0`},
		{name: "no running machine", active: `{"runs":[]}`, wantCode: 1, wantErr: "No running machine"},
		{name: "two running machines", active: `{"runs":[{"runId":"run_aaaaaaaaaaaaaaaaaaaaaaaa"},{"runId":"run_bbbbbbbbbbbbbbbbbbbbbbbb"}]}`, wantCode: 1, wantErr: "More than one running machine"},
		{name: "bad since", args: []string{"--since", "yesterday"}, wantCode: 1, wantErr: "--since must be"},
		{name: "bad until", args: []string{"--until", "soon"}, wantCode: 1, wantErr: "--until must be"},
		{name: "bad limit", args: []string{"--limit", "1441"}, wantCode: 1, wantErr: "--limit must be"},
		{name: "bad run", args: []string{"--run", "../x"}, wantCode: 1, wantErr: "--run must be"},
		{name: "server rejects cursor", args: []string{"--run", testMachineRunID, "--cursor", "bad"}, wantCode: 1, wantErr: "Invalid cursor"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			active = `{"runs":[{"runId":"` + testMachineRunID + `"}]}`
			if tc.active != "" {
				active = tc.active
			}
			a, out, errOut := testApp(t, server.URL, "", nil)
			saveTestCredential(t, a, credential)
			args := append(append([]string{"machine", "metrics", "--workstream", "694"}, tc.args...), "--agent", credential.AgentID)
			code := a.Run(args)
			if (code == 0) != (tc.wantCode == 0) {
				t.Fatalf("code=%d stderr=%s", code, errOut.String())
			}
			if !strings.Contains(out.String(), tc.wantOut) || !strings.Contains(errOut.String(), tc.wantErr) {
				t.Fatalf("stdout=%s stderr=%s", out.String(), errOut.String())
			}
		})
	}
}

func TestMetricsQueryDuration(t *testing.T) {
	now := time.Date(2026, 11, 7, 20, 0, 0, 0, time.UTC)
	q, err := metricsQuery("2h", "", 0, "", now)
	if err != nil || q.Get("since") != "2026-11-07T18:00:00Z" || q.Has("limit") {
		t.Fatalf("q=%v err=%v", q, err)
	}
}
