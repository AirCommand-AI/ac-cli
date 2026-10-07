package machinectl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

func TestPartialSeedDoesNotBlockHealthyAgent(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(credentials.Credential{APIToken: "api_good", AgentID: "agm_good", WorkstreamCode: "348", SocketAddress: "ac:agm_good", OrganizationID: "org_a"}); err != nil {
		t.Fatal(err)
	}
	manager := &fakeManager{defs: map[string]supervisor.AgentDefinition{
		"agm_good":    {AgentID: "agm_good", Name: "good", Desired: "running", State: "running", Mode: "tmux", Workstream: "348", WorkFolder: filepath.Join(home, "work", "good")},
		"agm_missing": {AgentID: "agm_missing", Name: "missing", Desired: "running", State: "running", Mode: "tmux", Workstream: "348", WorkFolder: filepath.Join(home, "work", "missing")},
	}}
	seeds := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/seed"):
			seeds++
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]string{{"agentId": "agm_good", "status": "seeded"}}})
		case strings.HasSuffix(req.URL.Path, "/agents"):
			if seeds == 0 {
				_ = json.NewEncoder(w).Encode(Definitions{Agents: []Agent{}})
				return
			}
			_ = json.NewEncoder(w).Encode(Definitions{Agents: []Agent{{AgentID: "agm_good", Name: "good", Desired: "running", Mode: "tmux", WorkFolder: manager.defs["agm_good"].WorkFolder, AssignedOrganizationID: "org_a", AssignedWorkstreamCode: "348", Revision: 1}}})
		case strings.HasSuffix(req.URL.Path, "/result"):
		default:
			t.Errorf("unexpected route %s", req.URL.Path)
		}
	}))
	defer server.Close()
	r := &AgentReconciler{API: HTTPAPI{BaseURL: server.URL, Client: server.Client(), Store: store}, Manager: manager, Store: store, Home: home}
	err := r.Reconcile(context.Background())
	if err == nil || !strings.Contains(err.Error(), "seed missing") {
		t.Fatalf("expected isolated seed failure: %v", err)
	}
	if seeds != 1 || manager.defs["agm_good"].Revision != 1 || len(manager.calls) != 0 {
		t.Fatalf("good agent stranded: seeds=%d def=%+v calls=%v", seeds, manager.defs["agm_good"], manager.calls)
	}
}

func TestLongFailureReasonAndRevisionBackoff(t *testing.T) {
	home := t.TempDir()
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
		t.Fatal(err)
	}
	manager := &fakeManager{defs: map[string]supervisor.AgentDefinition{}}
	attempts := 0
	results := 0
	revision := int64(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/agents"):
			_ = json.NewEncoder(w).Encode(Definitions{Agents: []Agent{{AgentID: "agm_new", Name: "eng-new", Desired: "running", Mode: "headless", WorkFolder: filepath.Join(home, "work", "new"), AssignedOrganizationID: "org_a", AssignedWorkstreamCode: "348", Revision: revision}}})
		case strings.HasSuffix(req.URL.Path, "/result"):
			results++
			var body struct {
				Reason string `json:"reason"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil || len(body.Reason) > 500 || !strings.Contains(body.Reason, "failed") {
				t.Errorf("reason length=%d err=%v", len(body.Reason), err)
			}
		default:
			t.Errorf("unexpected route %s", req.URL.Path)
		}
	}))
	defer server.Close()
	clock := time.Now()
	r := &AgentReconciler{API: HTTPAPI{BaseURL: server.URL, Client: server.Client(), Store: store}, Manager: manager, Store: store, Home: home, Now: func() time.Time { return clock }, Join: func(context.Context, Agent) error {
		attempts++
		return errors.New("failed " + strings.Repeat("é", 300))
	}}
	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("join failure lost")
	}
	if err := r.Reconcile(context.Background()); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("deferred retry must keep start failure visible: %v", err)
	}
	if attempts != 1 || results != 1 {
		t.Fatalf("same revision retried immediately: %d/%d", attempts, results)
	}
	revision = 2
	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("new revision should retry and fail")
	}
	if attempts != 2 || results != 2 {
		t.Fatalf("new revision ignored: %d/%d", attempts, results)
	}
}

func TestFailedCloneLeavesNoPartialCheckout(t *testing.T) {
	home := t.TempDir()
	folder := filepath.Join(home, "work", "new")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := prepareFolder(ctx, home, Agent{WorkFolder: folder, Repos: []string{"owner/repo"}})
	if err == nil {
		t.Fatal("cancelled clone succeeded")
	}
	entries, err := os.ReadDir(folder)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial clone left entries=%v error=%v", entries, err)
	}
	// A subsequent attempt must still see the destination as absent.
	if _, err := os.Lstat(filepath.Join(folder, "repo")); !os.IsNotExist(err) {
		t.Fatalf("partial checkout exists: %v", err)
	}
}

func TestLocalOperationGateBlocksReconcileUntilPost(t *testing.T) {
	gate := &sync.Mutex{}
	home := t.TempDir()
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(home, "work", "eng-1")
	manager := &fakeManager{defs: map[string]supervisor.AgentDefinition{"agm_1": {AgentID: "agm_1", Name: "eng-1", Desired: "running", State: "running", Mode: "tmux", Workstream: "348", WorkFolder: folder, Revision: 1}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/agents") {
			_ = json.NewEncoder(w).Encode(Definitions{Agents: []Agent{{AgentID: "agm_1", Name: "eng-1", Desired: "running", Mode: "tmux", WorkFolder: folder, AssignedOrganizationID: "org_a", AssignedWorkstreamCode: "348", Revision: 2}}})
		} else {
			t.Errorf("unexpected route %s", req.URL.Path)
		}
	}))
	defer server.Close()
	r := &AgentReconciler{Manager: manager, Store: store, Gate: gate, API: HTTPAPI{BaseURL: server.URL, Client: server.Client(), Store: store}}
	localReady, finishLocal := make(chan struct{}), make(chan struct{})
	localDone := make(chan struct{})
	go func() {
		gate.Lock()
		_ = manager.Stop(context.Background(), "eng-1")
		close(localReady)
		<-finishLocal // the local desired POST is still in flight
		_ = manager.MarkRevision("eng-1", 3)
		gate.Unlock()
		close(localDone)
	}()
	<-localReady
	done := make(chan error, 1)
	go func() { done <- r.Reconcile(context.Background()) }()
	select {
	case <-done:
		t.Fatal("reconcile passed unfinished local operation")
	case <-time.After(15 * time.Millisecond):
	}
	close(finishLocal)
	<-localDone
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reconcile did not release gate")
	}
	if manager.defs["agm_1"].Desired != "stopped" || manager.defs["agm_1"].Revision != 3 || len(manager.calls) != 1 {
		t.Fatalf("older server revision undid local stop: %+v calls=%v", manager.defs["agm_1"], manager.calls)
	}
}
