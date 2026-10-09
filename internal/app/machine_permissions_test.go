package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponseErrorAcceptsServerMessageShape(t *testing.T) {
	if got := responseError([]byte(`{"message":"This machine may start at most 2 agents."}`)); got != "This machine may start at most 2 agents." {
		t.Fatalf("message: %q", got)
	}
}

func TestMachineStartUsesDefaultsAndShowsPermissionError(t *testing.T) {
	cred := testCredential()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/agent/v1/workstreams/694/machine-runs" || r.Header.Get("Authorization") != "Bearer "+cred.APIToken {
			t.Errorf("request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body struct {
			Profile    string   `json:"profile"`
			AgentCount int      `json:"agentCount"`
			Runtime    int      `json:"runtimeLimitMin"`
			Repos      []string `json:"repos"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Profile != "Standard" || body.AgentCount != 2 || body.Runtime != 120 || len(body.Repos) != 1 {
			t.Errorf("body: %+v %v", body, err)
		}
		if calls == 1 {
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"runId":"run_1","state":"requested"}`))
		} else {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":"maxActive limit exceeded"}`))
		}
	}))
	defer server.Close()
	a, out, errs := testApp(t, server.URL, "", nil)
	saveTestCredential(t, a, cred)
	args := []string{"machine", "start", "--workstream", "694", "--agent", cred.AgentID, "--profile", "Standard", "--agents", "2", "--hours", "2", "--repo", "Org/repo"}
	if a.Run(args) != 0 || !strings.Contains(out.String(), "run_1 started: requested") {
		t.Fatalf("out=%s error=%s", out.String(), errs.String())
	}
	if a.Run(args) == 0 || !strings.Contains(errs.String(), "maxActive limit exceeded") {
		t.Fatalf("error=%s", errs.String())
	}
}
