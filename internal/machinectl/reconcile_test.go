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

type fakeManager struct {
	defs  map[string]supervisor.AgentDefinition
	calls []string
}

func (m *fakeManager) Definitions() []supervisor.AgentDefinition {
	out := make([]supervisor.AgentDefinition, 0, len(m.defs))
	for _, d := range m.defs {
		out = append(out, d)
	}
	return out
}
func (m *fakeManager) Start(_ context.Context, d supervisor.AgentDefinition) error {
	m.calls = append(m.calls, "start:"+d.Name)
	d.Desired = "running"
	d.State = "running"
	m.defs[d.AgentID] = d
	return nil
}
func (m *fakeManager) Stage(d supervisor.AgentDefinition) error {
	m.calls = append(m.calls, "stage:"+d.Name)
	d.Desired = "stopped"
	d.State = "stopped"
	m.defs[d.AgentID] = d
	return nil
}
func (m *fakeManager) Stop(_ context.Context, name string) error {
	m.calls = append(m.calls, "stop:"+name)
	for id, d := range m.defs {
		if d.Name == name {
			d.Desired = "stopped"
			d.State = "stopped"
			m.defs[id] = d
		}
	}
	return nil
}
func (m *fakeManager) Mode(_ context.Context, name, mode string) error {
	m.calls = append(m.calls, "mode:"+mode)
	for id, d := range m.defs {
		if d.Name == name {
			d.Mode = mode
			m.defs[id] = d
		}
	}
	return nil
}
func (m *fakeManager) MarkRevision(name string, revision int64) error {
	for id, d := range m.defs {
		if d.Name == name {
			d.Revision = revision
			m.defs[id] = d
		}
	}
	return nil
}

func TestReconcileSeedsWithoutRestartAndGatesByRevision(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(credentials.Credential{APIToken: "api_old", AgentID: "agm_1", WorkstreamCode: "348", SocketAddress: "ac:agm_1", OrganizationID: "org_from_credential"}); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(home, "work", "eng-1")
	manager := &fakeManager{defs: map[string]supervisor.AgentDefinition{"agm_1": {AgentID: "agm_1", Name: "eng-1", Desired: "running", State: "crashed", Mode: "tmux", Workstream: "348", WorkFolder: folder, Revision: 0}}}
	revision := int64(1)
	mode := "tmux"
	var seedCount, reports int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer device" {
			t.Errorf("machine authorization missing: %s", req.Header.Get("Authorization"))
		}
		switch {
		case req.URL.Path == "/agent/v1/machines/me/agents/seed":
			seedCount++
			var body struct {
				Agents []Seed `json:"agents"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body.Agents) != 1 || body.Agents[0].AssignedOrganizationID != "org_from_credential" {
				t.Errorf("seed = %+v", body)
			}
		case req.URL.Path == "/agent/v1/machines/me/agents":
			// A previously joined row can have empty assigned* fields;
			// seeding must retain its local organization and workstream.
			_ = json.NewEncoder(w).Encode(Definitions{Agents: []Agent{{AgentID: "agm_1", Name: "eng-1", Desired: "running", Mode: mode, WorkFolder: folder, JoinedOrganizationID: "org_from_credential", JoinedWorkstreamCode: "348", Revision: revision}}})
		case strings.HasSuffix(req.URL.Path, "/result"):
			reports++
		default:
			t.Errorf("unexpected route %s", req.URL.Path)
		}
	}))
	defer server.Close()
	r := &AgentReconciler{API: HTTPAPI{BaseURL: server.URL, Client: server.Client(), Store: store}, Manager: manager, Store: store, Home: home, Join: func(context.Context, Agent) error { return nil }}
	ctx := context.Background()
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if seedCount != 1 || reports != 1 || len(manager.calls) != 0 || manager.defs["agm_1"].State != "crashed" {
		t.Fatalf("seed=%d report=%d calls=%v definition=%+v", seedCount, reports, manager.calls, manager.defs["agm_1"])
	}
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if reports != 1 || seedCount != 1 {
		t.Fatalf("unchanged revision applied: reports=%d seeds=%d", reports, seedCount)
	}
	revision = 2
	mode = "headless"
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(manager.calls, ",") != "stop:eng-1,mode:headless,start:eng-1" {
		t.Fatalf("mode transition: %v", manager.calls)
	}
	// A later explicit revision resumes a parked agent even when desired
	// remains running; the periodic check at the same revision did not.
	d := manager.defs["agm_1"]
	d.State = "crashed"
	manager.defs["agm_1"] = d
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(manager.calls) != 3 {
		t.Fatalf("parked state restarted without revision: %v", manager.calls)
	}
	revision = 3
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(manager.calls, ",") != "stop:eng-1,mode:headless,start:eng-1,start:eng-1" {
		t.Fatalf("explicit resume: %v", manager.calls)
	}
}

func TestReconcileCreatesOnlyNewAgentAndReports(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(home, "work", "eng-new")
	reports := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/seed"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(req.URL.Path, "/agents"):
			_ = json.NewEncoder(w).Encode(Definitions{Agents: []Agent{{AgentID: "agm_new", Name: "eng-new", Desired: "running", Mode: "headless", WorkFolder: folder, AssignedOrganizationID: "org_a", AssignedWorkstreamCode: "348", Revision: 1}}})
		case strings.HasSuffix(req.URL.Path, "/result"):
			reports++
		default:
			t.Errorf("unexpected route %s", req.URL.Path)
		}
	}))
	defer server.Close()
	manager := &fakeManager{defs: make(map[string]supervisor.AgentDefinition)}
	joins := 0
	r := &AgentReconciler{API: HTTPAPI{BaseURL: server.URL, Client: server.Client(), Store: store}, Manager: manager, Store: store, Home: home, Join: func(context.Context, Agent) error { joins++; return nil }}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if joins != 1 || reports != 1 || strings.Join(manager.calls, ",") != "start:eng-new" {
		t.Fatalf("joins=%d reports=%d calls=%v", joins, reports, manager.calls)
	}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if joins != 1 || reports != 1 {
		t.Fatalf("applied twice: joins=%d reports=%d", joins, reports)
	}
}
