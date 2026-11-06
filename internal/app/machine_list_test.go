package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testMachineRunID = "run_0123456789abcdef01234567"

func TestMachineListAndStatus(t *testing.T) {
	credential := testCredential()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+credential.APIToken {
			t.Errorf("method=%s bearer=%q", r.Method, r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/agent/v1/workstreams/694/machine-runs":
			if r.URL.RawQuery == "cursor=bad&limit=20&state=all" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":"Invalid cursor","code":"BadRequest"}`))
				return
			}
			if got := r.URL.Query(); got.Get("state") != "active" || got.Get("limit") != "5" || got.Get("cursor") != "c1" {
				t.Errorf("query=%s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"runs":[{"runId":"` + testMachineRunID + `","state":"running","hardLimitAt":"2026-11-06T20:00:00Z"}],"nextCursor":"c2"}`))
		case "/agent/v1/workstreams/694/machine-runs/" + testMachineRunID:
			_, _ = w.Write([]byte(`{"runId":"` + testMachineRunID + `","state":"finished","endedAt":"2026-11-06T19:00:00Z"}`))
		case "/agent/v1/workstreams/694/machine-runs/run_ffffffffffffffffffffffff":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Machine run not found","code":"NotFound"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{name: "list page", args: []string{"machine", "list", "--workstream", "694", "--state", "active", "--limit", "5", "--cursor", "c1"}, wantOut: `"nextCursor":"c2"`},
		{name: "status", args: []string{"machine", "status", "--workstream", "694", "--run", testMachineRunID}, wantOut: `"endedAt":"2026-11-06T19:00:00Z"`},
		{name: "status unknown run", args: []string{"machine", "status", "--workstream", "694", "--run", "run_ffffffffffffffffffffffff"}, wantCode: 1, wantErr: "Machine run not found"},
		{name: "bad cursor", args: []string{"machine", "list", "--workstream", "694", "--cursor", "bad"}, wantCode: 1, wantErr: "Invalid cursor"},
		{name: "bad state", args: []string{"machine", "list", "--workstream", "694", "--state", "running"}, wantCode: 1, wantErr: "--state must be"},
		{name: "bad limit", args: []string{"machine", "list", "--workstream", "694", "--limit", "101"}, wantCode: 1, wantErr: "--limit must be"},
		{name: "bad run id", args: []string{"machine", "status", "--workstream", "694", "--run", "../x"}, wantCode: 1, wantErr: "--run must be"},
		{name: "missing run", args: []string{"machine", "status", "--workstream", "694"}, wantCode: 1, wantErr: "machine status"},
		{name: "missing workstream", args: []string{"machine", "list"}, wantCode: 1, wantErr: "machine list"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, out, errOut := testApp(t, server.URL, "", nil)
			saveTestCredential(t, a, credential)
			args := append(append([]string{}, tc.args...), "--agent", credential.AgentID)
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
