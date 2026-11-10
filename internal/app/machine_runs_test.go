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
	if code := a.Run([]string{"machine", "request", "--workstream", "694", "--agent", credential.AgentID, "--profile", "standard"}); code == 0 {
		t.Fatal("run without repositories accepted")
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

func TestMachineStartTeamSelection(t *testing.T) {
	credential := testCredential()
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"runId":"run_0123456789abcdef01234567","state":"requested"}`))
	}))
	defer server.Close()
	a, _, errOut := testApp(t, server.URL, "", nil)
	saveTestCredential(t, a, credential)
	base := []string{"machine", "start", "--workstream", "694", "--agent", credential.AgentID, "--profile", "standard", "--repo", "Org/repo"}
	tests := []struct {
		name  string
		extra []string
		ok    bool
		check func(map[string]any) bool
	}{
		{"named team", []string{"--team", "Claude lead + 3"}, true, func(b map[string]any) bool { return b["team"] == "Claude lead + 3" && b["agentCount"] == nil }},
		{"default team", nil, true, func(b map[string]any) bool {
			return b["team"] == nil && b["agentCount"] == nil && b["agentNames"] == nil
		}},
		{"agent count", []string{"--agents", "3"}, true, func(b map[string]any) bool { return b["agentCount"] == float64(3) && b["team"] == nil }},
		{"team and count", []string{"--team", "x", "--agents", "2"}, false, nil},
		{"team and names", []string{"--team", "x", "--agent-name", "eng-a"}, false, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := len(bodies)
			code := a.Run(append(append([]string{}, base...), tc.extra...))
			if (code == 0) != tc.ok {
				t.Fatalf("exit %d: %s", code, errOut.String())
			}
			if !tc.ok {
				if len(bodies) != before {
					t.Fatal("rejected request was sent")
				}
				return
			}
			if len(bodies) != before+1 || !tc.check(bodies[before]) {
				t.Fatalf("body %v", bodies[len(bodies)-1])
			}
		})
	}
}

// Git also calls a credential helper with store (after a login worked) and
// erase (after it failed). The daemon issues the token, so both are accepted
// and ignored; only get needs a run machine.
func TestGitCredentialAcceptsStoreAndErase(t *testing.T) {
	for _, op := range []string{"store", "erase"} {
		t.Run(op, func(t *testing.T) {
			a, out, errOut := testApp(t, "http://127.0.0.1:1", "", nil)
			a.Stdin = strings.NewReader("protocol=https\nhost=github.com\nusername=x-access-token\npassword=secret\n\n")
			if code := a.Run([]string{"git-credential", op}); code != 0 {
				t.Fatalf("exit %d: %s", code, errOut.String())
			}
			if out.Len() != 0 || errOut.Len() != 0 {
				t.Fatalf("output %q %q", out.String(), errOut.String())
			}
		})
	}
	a, _, errOut := testApp(t, "http://127.0.0.1:1", "", nil)
	if code := a.Run([]string{"git-credential", "fetch"}); code == 0 || !strings.Contains(errOut.String(), "git-credential get") {
		t.Fatalf("unknown operation: exit %d %q", code, errOut.String())
	}
}
