package machinectl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

func TestSeedParsesMixedAndMissingResults(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
		t.Fatal(err)
	}
	response := map[string]any{"results": []map[string]string{{"agentId": "agm_good", "status": "seeded"}, {"agentId": "agm_bad", "status": "skipped", "reason": "Agent is missing or inactive"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { _ = json.NewEncoder(w).Encode(response) }))
	defer server.Close()
	api := HTTPAPI{BaseURL: server.URL, Client: server.Client(), Store: store}
	err := api.Seed(context.Background(), []Seed{{AgentID: "agm_good"}, {AgentID: "agm_bad"}})
	var failure *SeedResultError
	if !errors.As(err, &failure) || failure.AgentID != "agm_bad" || !failure.Permanent() || strings.Contains(err.Error(), "agm_good") {
		t.Fatalf("mixed seed results: %v", err)
	}
	response = map[string]any{"results": []map[string]string{{"agentId": "agm_good", "status": "seeded"}}}
	err = api.Seed(context.Background(), []Seed{{AgentID: "agm_good"}, {AgentID: "agm_bad"}})
	if !errors.As(err, &failure) || failure.AgentID != "agm_bad" || failure.Permanent() {
		t.Fatalf("missing result accepted: %v", err)
	}
	response = map[string]any{"results": []map[string]string{{"agentId": "agm_good", "status": "seeded"}, {"agentId": "agm_good", "status": "seeded"}}}
	if err = api.Seed(context.Background(), []Seed{{AgentID: "agm_good"}}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate result accepted: %v", err)
	}
}

func TestReconcileIsolatesSkippedSeedAndAppliesHealthyAgent(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
		t.Fatal(err)
	}
	manager := &fakeManager{defs: map[string]supervisor.AgentDefinition{}}
	for _, id := range []string{"agm_good", "agm_bad"} {
		if err := store.Save(credentials.Credential{APIToken: "api_" + id, AgentID: id, WorkstreamCode: "348", OrganizationID: "org_a", SocketAddress: "ac:" + id}); err != nil {
			t.Fatal(err)
		}
		manager.defs[id] = supervisor.AgentDefinition{AgentID: id, Name: id, Desired: "running", State: "running", Mode: "tmux", Workstream: "348", WorkFolder: filepath.Join(home, "work", id)}
	}
	var mu sync.Mutex
	seeds := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/seed"):
			var input struct {
				Agents []Seed `json:"agents"`
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil || len(input.Agents) != 1 {
				t.Errorf("seed input: %+v %v", input, err)
				return
			}
			id := input.Agents[0].AgentID
			mu.Lock()
			seeds[id]++
			mu.Unlock()
			status := "seeded"
			if id == "agm_bad" {
				status = "skipped"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]string{{"agentId": id, "status": status, "reason": "Agent is missing or inactive"}}})
		case strings.HasSuffix(req.URL.Path, "/agents"):
			mu.Lock()
			seeded := seeds["agm_good"] > 0
			mu.Unlock()
			var agents []Agent
			if seeded {
				agents = append(agents, Agent{AgentID: "agm_good", Name: "agm_good", Desired: "running", Mode: "tmux", WorkFolder: manager.defs["agm_good"].WorkFolder, AssignedOrganizationID: "org_a", AssignedWorkstreamCode: "348", Revision: 1})
			}
			_ = json.NewEncoder(w).Encode(Definitions{Agents: agents})
		case strings.HasSuffix(req.URL.Path, "/result"):
		default:
			t.Errorf("unexpected route: %s", req.URL.Path)
		}
	}))
	defer server.Close()
	r := &AgentReconciler{API: HTTPAPI{BaseURL: server.URL, Client: server.Client(), Store: store}, Manager: manager, Store: store, Home: home}
	if err := r.Reconcile(context.Background()); err == nil || !strings.Contains(err.Error(), "agm_bad") {
		t.Fatalf("skipped seed not surfaced: %v", err)
	}
	if manager.defs["agm_good"].Revision != 1 {
		t.Fatalf("healthy agent not reconciled: %+v", manager.defs["agm_good"])
	}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("rejected seed retried: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if seeds["agm_good"] != 1 || seeds["agm_bad"] != 1 {
		t.Fatalf("seeds retried: %+v", seeds)
	}
}
