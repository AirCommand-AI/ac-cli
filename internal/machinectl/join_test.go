package machinectl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func TestFreshJoinOnRevokedBearer(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(credentials.Credential{APIToken: "api_revoked", AgentID: "agm_1", WorkstreamCode: "348", SocketAddress: "ac:agm_1", OrganizationID: "org_a"}); err != nil {
		t.Fatal(err)
	}
	joined := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/agent/v1/workstreams/348":
			if req.Header.Get("Authorization") != "Bearer api_revoked" {
				t.Errorf("old bearer missing")
			}
			w.WriteHeader(http.StatusUnauthorized)
		case "/v1/agents/agm_1/workstreams/348":
			joined++
			if req.Header.Get("Authorization") != "Bearer device" || req.Header.Get("X-AC-Organization") != "org_a" {
				t.Errorf("join auth/organization wrong")
			}
			var payload struct {
				APIToken string `json:"apiToken"`
			}
			if err := json.NewDecoder(req.Body).Decode(&payload); err != nil || payload.APIToken == "api_revoked" || payload.APIToken == "" {
				t.Errorf("new bearer: %+v %v", payload, err)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"agentId": "agm_1", "agentName": "eng-1", "workstreamCode": "348", "socketAddress": "ac:agm_1"})
		default:
			t.Errorf("unexpected path %s", req.URL.Path)
		}
	}))
	defer server.Close()
	r := &AgentReconciler{API: HTTPAPI{BaseURL: server.URL, Client: server.Client(), Store: store}, Store: store}
	if err := r.joinHTTP(context.Background(), Agent{AgentID: "agm_1", AssignedOrganizationID: "org_a", AssignedWorkstreamCode: "348"}); err != nil {
		t.Fatal(err)
	}
	cred, err := store.FindByAgent("348", "agm_1")
	if err != nil || cred.APIToken == "api_revoked" || joined != 1 {
		t.Fatalf("cred=%+v err=%v joins=%d", cred, err, joined)
	}
}

func TestDesiredUsesMachineCredential(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/agent/v1/machines/me/agents/agm_1/desired" || req.Header.Get("Authorization") != "Bearer device" {
			t.Errorf("request %s %s", req.URL.Path, req.Header.Get("Authorization"))
		}
		var body struct {
			Desired string `json:"desired"`
			Mode    string `json:"mode"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body.Desired != "stopped" || body.Mode != "tmux" {
			t.Errorf("body %+v %v", body, err)
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"revision": 9})
	}))
	defer server.Close()
	api := HTTPAPI{BaseURL: server.URL, Client: server.Client(), Store: store}
	rev, err := api.Desired(context.Background(), "agm_1", "stopped", "tmux")
	if err != nil || rev != 9 {
		t.Fatalf("revision %d %v", rev, err)
	}
}
