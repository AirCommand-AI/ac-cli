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

func TestRejectedSeedIsNotRetriedEveryCheck(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusConflict} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			home := t.TempDir()
			store := credentials.NewStore(home)
			if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(credentials.Credential{APIToken: "api_old", AgentID: "agm_1", WorkstreamCode: "348", OrganizationID: "org_a", SocketAddress: "ac:agm_1"}); err != nil {
				t.Fatal(err)
			}
			manager := &fakeManager{defs: map[string]supervisor.AgentDefinition{"agm_1": {AgentID: "agm_1", Name: "eng-1", Desired: "running", Mode: "tmux", Workstream: "348", WorkFolder: filepath.Join(home, "work", "eng-1")}}}
			seeds := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch {
				case strings.HasSuffix(req.URL.Path, "/agents"):
					_ = json.NewEncoder(w).Encode(Definitions{Agents: []Agent{}})
				case strings.HasSuffix(req.URL.Path, "/seed"):
					seeds++
					w.WriteHeader(status)
				default:
					t.Errorf("unexpected route %s", req.URL.Path)
				}
			}))
			defer server.Close()
			r := &AgentReconciler{API: HTTPAPI{BaseURL: server.URL, Client: server.Client(), Store: store}, Manager: manager, Store: store, Home: home}
			if err := r.Reconcile(context.Background()); err == nil {
				t.Fatal("seed rejection unreported")
			}
			if err := r.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			if seeds != 1 {
				t.Fatalf("permanently rejected seed attempted %d times", seeds)
			}
		})
	}
}

func TestPendingPostSkipsOnlyAffectedAgent(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
		t.Fatal(err)
	}
	manager := &fakeManager{defs: map[string]supervisor.AgentDefinition{}, pendingSkip: map[string]bool{"agm_1": true}}
	var agents []Agent
	for i, name := range []string{"eng-1", "eng-2"} {
		id := []string{"agm_1", "agm_2"}[i]
		folder := filepath.Join(home, "work", name)
		manager.defs[id] = supervisor.AgentDefinition{AgentID: id, Name: name, Desired: "running", State: "running", Mode: "tmux", Workstream: "348", WorkFolder: folder, Revision: 1}
		agents = append(agents, Agent{AgentID: id, Name: name, Desired: "stopped", Mode: "tmux", WorkFolder: folder, AssignedOrganizationID: "org_a", AssignedWorkstreamCode: "348", Revision: 2})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/agents"):
			_ = json.NewEncoder(w).Encode(Definitions{Agents: agents})
		case strings.HasSuffix(req.URL.Path, "/result"):
		default:
			t.Errorf("unexpected route %s", req.URL.Path)
		}
	}))
	defer server.Close()
	r := &AgentReconciler{API: HTTPAPI{BaseURL: server.URL, Client: server.Client(), Store: store}, Manager: manager, Store: store, Home: home}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if manager.defs["agm_1"].Desired != "running" || manager.defs["agm_2"].Desired != "stopped" || manager.defs["agm_2"].Revision != 2 {
		t.Fatalf("skip blocked other agent: %+v", manager.defs)
	}
}
