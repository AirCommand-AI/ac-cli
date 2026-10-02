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

func TestResultConflictAndOutageDoNotAdvanceRevision(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			home := t.TempDir()
			store := credentials.NewStore(home)
			if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
				t.Fatal(err)
			}
			folder := filepath.Join(home, "work", "new")
			manager := &fakeManager{defs: map[string]supervisor.AgentDefinition{}}
			results := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch {
				case strings.HasSuffix(req.URL.Path, "/agents"):
					_ = json.NewEncoder(w).Encode(Definitions{Agents: []Agent{{AgentID: "agm_new", Name: "new", Desired: "running", Mode: "headless", WorkFolder: folder, AssignedOrganizationID: "org_a", AssignedWorkstreamCode: "348", Revision: 1}}})
				case strings.HasSuffix(req.URL.Path, "/result"):
					results++
					if results == 1 {
						w.WriteHeader(status)
					}
				default:
					t.Errorf("unexpected route %s", req.URL.Path)
				}
			}))
			defer server.Close()
			r := &AgentReconciler{API: HTTPAPI{BaseURL: server.URL, Client: server.Client(), Store: store}, Manager: manager, Store: store, Home: home, Join: func(context.Context, Agent) error { return nil }}
			if err := r.Reconcile(context.Background()); err == nil {
				t.Fatal("failed result accepted")
			}
			if manager.defs["agm_new"].Revision != 0 {
				t.Fatal("revision advanced despite result failure")
			}
			if err := r.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			if manager.defs["agm_new"].Revision != 1 || results != 2 || len(manager.calls) != 1 {
				t.Fatalf("revision=%d results=%d calls=%v", manager.defs["agm_new"].Revision, results, manager.calls)
			}
		})
	}
}
