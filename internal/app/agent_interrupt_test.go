package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func TestMachineInterruptRetryReusesIdempotencyID(t *testing.T) {
	var ids []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/agents" {
			_, _ = w.Write([]byte(`{"agents":[{"agentId":"agm_1","name":"eng-1","status":"active","workstreamCode":"980"}]}`))
			return
		}
		var body struct {
			IdempotencyID string `json:"idempotencyId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		ids = append(ids, body.IdempotencyID)
		if len(ids) == 1 {
			h := w.(http.Hijacker)
			conn, _, _ := h.Hijack()
			_ = conn.Close()
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	a, _, _ := testApp(t, server.URL, "", nil)
	storedMachine(t, a)
	a.RetryAttempts = 2
	a.RetryDelay = func(int) {}
	if err := a.Store.Save(credentials.Credential{AgentID: "agm_1", AgentName: "eng-1", OrganizationID: "org_1", WorkstreamCode: "980", APIToken: "api_test", SocketAddress: "ac:agm_1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.interruptAgent("eng-1", "stop"); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("ids %v", ids)
	}
}
func TestMachineInterruptFailureIncludesServerCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/agents" {
			_, _ = w.Write([]byte(`{"agents":[{"agentId":"agm_1","name":"eng-1","status":"active","workstreamCode":"980"}]}`))
			return
		}
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":{"code":"AgentStopped","message":"agent is stopped"}}`))
	}))
	defer server.Close()
	a, _, _ := testApp(t, server.URL, "", nil)
	storedMachine(t, a)
	if err := a.Store.Save(credentials.Credential{AgentID: "agm_1", AgentName: "eng-1", OrganizationID: "org_1", WorkstreamCode: "980", APIToken: "api_test", SocketAddress: "ac:agm_1"}); err != nil {
		t.Fatal(err)
	}
	err := a.interruptAgent("eng-1", "stop")
	if err == nil || !strings.Contains(err.Error(), "AgentStopped: agent is stopped") {
		t.Fatalf("error: %v", err)
	}
}
func TestMachineInterruptRouteAndOrganization(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/agents":
			_, _ = w.Write([]byte(`{"agents":[{"agentId":"agm_1","name":"eng-1","status":"active","workstreamCode":"980"}]}`))
		case "/agent/v1/machines/me/agents/agm_1/interrupt":
			calls++
			if r.Method != http.MethodPost || r.Header.Get("X-AC-Organization") != "org_1" || r.Header.Get("Authorization") != "Bearer sk-ac-abcdefghijklmnopqrstuvwxyz012345" {
				t.Errorf("route headers: %v", r.Header)
			}
			var body struct {
				Text          string `json:"text"`
				IdempotencyID string `json:"idempotencyId"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text != "stop now" || body.IdempotencyID == "" {
				t.Errorf("body: %+v %v", body, err)
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	a, _, _ := testApp(t, server.URL, "", nil)
	storedMachine(t, a)
	if err := a.Store.Save(credentials.Credential{AgentID: "agm_1", AgentName: "eng-1", OrganizationID: "org_1", WorkstreamCode: "980", APIToken: "api_test", SocketAddress: "ac:agm_1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.interruptAgent("eng-1", "stop now"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls %d", calls)
	}
}
