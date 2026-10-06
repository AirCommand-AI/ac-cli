package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMachineRunAgentCommandsUseAgentBearerAndC5Body(t *testing.T) {
	credential := testCredential()
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer "+credential.APIToken {
			t.Errorf("wrong bearer %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/agent/v1/workstreams/694/machine-runs":
			var body struct {
				Profile         string   `json:"profile"`
				AgentCount      int      `json:"agentCount"`
				Repos           []string `json:"repos"`
				RuntimeLimitMin int      `json:"runtimeLimitMin"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Profile != "standard" || body.AgentCount != 2 || len(body.Repos) != 1 || body.Repos[0] != "Org/repo" || body.RuntimeLimitMin != 90 {
				t.Errorf("bad body %+v", body)
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"runId":"run_0123456789abcdef01234567","state":"requested"}`))
		case "/agent/v1/workstreams/694/machine-runs/run_0123456789abcdef01234567/done", "/agent/v1/workstreams/694/machine-runs/run_0123456789abcdef01234567/cancel":
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	a, out, errOut := testApp(t, server.URL, "", nil)
	saveTestCredential(t, a, credential)
	for _, args := range [][]string{{"machine", "request", "--workstream", "694", "--agent", credential.AgentID, "--profile", "standard", "--agents", "2", "--repo", "Org/repo", "--runtime-min", "90"}, {"machine", "done", "--workstream", "694", "--agent", credential.AgentID, "--run", "run_0123456789abcdef01234567"}, {"machine", "cancel", "--workstream", "694", "--agent", credential.AgentID, "--run", "run_0123456789abcdef01234567"}} {
		if code := a.Run(args); code != 0 {
			t.Fatalf("%v: %s", args, errOut.String())
		}
	}
	if len(paths) != 3 || !strings.Contains(out.String(), "run_0123456789abcdef01234567") {
		t.Fatalf("paths=%v output=%s", paths, out.String())
	}
}
func TestMachineRunApprovalScopeFlags(t *testing.T) {
	credential := testCredential()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Action string `json:"action"`
			Scope  struct {
				Actions       []string `json:"actions"`
				MaxAgents     int      `json:"maxAgents"`
				MaxRuntimeMin int      `json:"maxRuntimeMin"`
				Profile       string   `json:"profile"`
				Repos         []string `json:"repos"`
				Allow         bool     `json:"allowFromRunMachine"`
			} `json:"scope"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Action != "machine.run" || len(body.Scope.Actions) != 1 || body.Scope.Actions[0] != "machine.run" || body.Scope.MaxAgents != 2 || body.Scope.MaxRuntimeMin != 90 || body.Scope.Profile != "standard" || len(body.Scope.Repos) != 1 || !body.Scope.Allow {
			t.Errorf("scope=%+v", body)
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"requestId":"req_test","agentId":"` + credential.AgentID + `","status":"pending"}`))
	}))
	defer server.Close()
	a, _, errs := testApp(t, server.URL, "", nil)
	saveTestCredential(t, a, credential)
	if code := a.Run([]string{"approval", "request", "--workstream", "694", "--agent", credential.AgentID, "--action", "machine.run", "--max-agents", "2", "--max-runtime-min", "90", "--profile", "standard", "--repo", "Org/repo", "--allow-from-run-machine"}); code != 0 {
		t.Fatal(errs.String())
	}
}
