package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

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
