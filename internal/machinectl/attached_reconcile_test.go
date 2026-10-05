package machinectl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

type attachedFakeManager struct{ *fakeManager }

func (m *attachedFakeManager) Attached(id string) (supervisor.AgentDefinition, bool) {
	if id == "agm_attached" {
		return supervisor.AgentDefinition{AgentID: id, Kind: "attached"}, true
	}
	return supervisor.AgentDefinition{}, false
}
func TestReconcileNeverStagesAnAttachedAgent(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/v1/machines/me/agents" {
			t.Errorf("unexpected reconcile operation %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(Definitions{Agents: []Agent{{AgentID: "agm_attached", Name: "person", Desired: "running", Mode: "headless", AssignedOrganizationID: "org_a", AssignedWorkstreamCode: "478", Revision: 3}}})
	}))
	defer server.Close()
	m := &attachedFakeManager{&fakeManager{defs: map[string]supervisor.AgentDefinition{}}}
	r := &AgentReconciler{API: HTTPAPI{BaseURL: server.URL, Client: server.Client(), Store: store}, Store: store, Manager: m, Home: home}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 0 {
		t.Fatalf("attached agent reconciled as started: %v", m.calls)
	}
}
func TestJoinAttachedAssignmentWithoutDefinition(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agents/agm_attached/workstreams/478" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer device" {
			t.Error("missing machine token")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"agentId": "agm_attached", "agentName": "person", "workstreamCode": "478", "socketAddress": "ac:agm_attached"})
	}))
	defer server.Close()
	r := &AgentReconciler{API: HTTPAPI{BaseURL: server.URL, Client: server.Client(), Store: store}, Store: store}
	if err := r.JoinAssignment(context.Background(), Agent{AgentID: "agm_attached", AssignedOrganizationID: "org_a", AssignedWorkstreamCode: "478"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindByAgent("478", "agm_attached"); err != nil {
		t.Fatal(err)
	}
}
