package machinectl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

func TestReconcileSavesRoleAndPromptWithoutRestart(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(home, "work", "lead")
	manager := &fakeManager{defs: map[string]supervisor.AgentDefinition{"agm_1": {AgentID: "agm_1", Name: "lead", Desired: "running", State: "running", Mode: "headless", Workstream: "766", WorkFolder: folder, Revision: 4}}}
	server := Agent{AgentID: "agm_1", Name: "lead", Desired: "running", Mode: "headless", WorkFolder: folder, AssignedOrganizationID: "org_1", AssignedWorkstreamCode: "766", Revision: 4, Role: "eng-lead", RoleLabel: "Eng Lead", RolePrompt: "Lead well.", RolePromptRev: 2, Harness: "pi", Model: "gpt-6-sol", Effort: "high", OrganizationName: "Builder Magic"}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/agent/v1/machines/me/agents":
			_ = json.NewEncoder(w).Encode(Definitions{Agents: []Agent{server}})
		case strings.HasSuffix(req.URL.Path, "/result"):
		default:
			t.Errorf("unexpected route %s", req.URL.Path)
		}
	}))
	defer api.Close()
	r := &AgentReconciler{API: HTTPAPI{BaseURL: api.URL, Client: api.Client(), Store: store}, Manager: manager, Store: store, Home: home, Join: func(context.Context, Agent) error { return nil }}
	ctx := context.Background()
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	p := manager.defs["agm_1"].Profile
	if p == nil || p.Role != "eng-lead" || p.RolePrompt != "Lead well." || p.Model != "gpt-6-sol" || p.Effort != "high" || p.OrganizationName != "Builder Magic" {
		t.Fatalf("profile = %+v", p)
	}
	if strings.Join(manager.calls, ",") != "profile:lead" {
		t.Fatalf("calls = %v (a role change must not stop or start the agent)", manager.calls)
	}
	// A new prompt revision with the same definition revision is applied too.
	server.RolePrompt, server.RolePromptRev = "Lead better.", 3
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if manager.defs["agm_1"].Profile.RolePromptRev != 3 || strings.Join(manager.calls, ",") != "profile:lead,profile:lead" {
		t.Fatalf("prompt update: %+v %v", manager.defs["agm_1"].Profile, manager.calls)
	}
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(manager.calls) != 2 {
		t.Fatalf("unchanged profile written again: %v", manager.calls)
	}
}

func TestDefinitionCarriesProfile(t *testing.T) {
	d := definition(Agent{AgentID: "agm_1", Name: "eng-1", Harness: "pi", Role: "engineer", Model: "gpt-6-sol"})
	if d.Harness != "pi" || d.Profile == nil || d.Profile.Role != "engineer" || d.Profile.Model != "gpt-6-sol" {
		t.Fatalf("definition = %+v", d)
	}
	if definition(Agent{AgentID: "agm_1", Name: "eng-1"}).Profile != nil {
		t.Fatal("no role or model settings must give no profile")
	}
}
