package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMachinePermissionsAndCreateUseMachineBearerWithAgentIdentity(t *testing.T) {
	agent := agentSummary{AgentID: "agm_1", Name: "worker", Status: "active", OrganizationID: "org_aaaaaaaaaaaaaaaaaaaaaaaaaa", WorkstreamCode: "694"}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agents":
			json.NewEncoder(w).Encode(listAgentsResponse{Agents: []agentSummary{agent}})
		case "/v1/organizations":
			w.Write([]byte(`{"organizations":[{"organizationId":"org_aaaaaaaaaaaaaaaaaaaaaaaaaa","name":"Acme"}]}`))
		case "/agent/v1/workstreams":
			calls++
			if r.Header.Get("Authorization") != "Bearer sk-ac-abcdefghijklmnopqrstuvwxyz012345" || r.Header.Get("X-Agent-ID") != agent.AgentID {
				t.Errorf("bad create auth: %+v", r.Header)
			}
			var body struct {
				Workspace   string `json:"workspace"`
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Workspace != agent.OrganizationID || body.Name != "new stream" || body.Description != "summary" {
				t.Errorf("body %+v", body)
			}
			w.WriteHeader(201)
			w.Write([]byte(`{"code":"715"}`))
		case "/agent/v1/machines/me/permissions":
			calls++
			if r.Header.Get("X-Agent-ID") != agent.AgentID || r.Header.Get("Authorization") != "Bearer sk-ac-abcdefghijklmnopqrstuvwxyz012345" {
				t.Errorf("bad list auth: %+v", r.Header)
			}
			w.Write([]byte(`{"permissions":[{"id":"mp_1","kind":"workstream.create","workspaces":["org_aaaaaaaaaaaaaaaaaaaaaaaaaa"],"workspaceNames":["Acme"],"defaultGrants":[{"action":"work.start"}],"limits":null},{"id":"mp_2","kind":"machine.run","profileNames":["Standard"],"limits":{"profileIds":["mp_profile"],"maxAgents":4,"maxRuntimeMin":300,"maxActive":1}}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	a, out, errs := testApp(t, server.URL, "", nil)
	storedMachine(t, a)
	if a.Run([]string{"permissions", "--agent", "worker"}) != 0 {
		t.Fatal(errs.String())
	}
	if a.Run([]string{"workstream", "create", "--workspace", "Acme", "--name", "new stream", "--description", "summary", "--agent", "worker"}) != 0 {
		t.Fatal(errs.String())
	}
	if calls != 2 || !strings.Contains(out.String(), "Create workstreams in: Acme (default grants: Start work on tasks); no expiry") || !strings.Contains(out.String(), "Start cloud machines: Standard; up to 4 agents, 5 h, 1 at once; no expiry") || strings.Contains(out.String(), "mp_1") || !strings.Contains(out.String(), "still in workstream 694") {
		t.Fatalf("calls=%d out=%s", calls, out.String())
	}
}
