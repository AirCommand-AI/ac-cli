package machinectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

type fakeTokens struct {
	mu      sync.Mutex
	calls   []string
	err     error
	entered chan struct{}
	release chan struct{}
}

func (f *fakeTokens) For(_ context.Context, owner string, force bool) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fmt.Sprintf("%s:%v", owner, force))
	err := f.err
	f.mu.Unlock()
	if f.entered != nil {
		f.entered <- struct{}{}
		<-f.release
	}
	return "tok", err
}

func (f *fakeTokens) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeRepoReports struct {
	reports []string
	err     error
}

func (f *fakeRepoReports) ReportRepo(_ context.Context, agentID, repo string, ok bool, reason string) error {
	f.reports = append(f.reports, fmt.Sprintf("%s %s ok=%v %s", agentID, repo, ok, reason))
	return f.err
}

type addRepoEnv struct {
	home, folder, origin string
	now                  time.Time
	remote               []Agent
	gate                 *sync.Mutex
	manager              *fakeManager
	tokens               *fakeTokens
	reports              *fakeRepoReports
	r                    *AgentReconciler
}

// newAddRepoEnv has agent eng-1 with owner/first saved locally; the server
// adds Other/second without bumping the revision. With created=false the
// agent exists only on the server and is created by the reconcile.
func newAddRepoEnv(t *testing.T, created bool) *addRepoEnv {
	t.Helper()
	home := t.TempDir()
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
		t.Fatal(err)
	}
	e := &addRepoEnv{home: home, folder: filepath.Join(home, "work", "eng-1"), origin: t.TempDir(), now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC), gate: &sync.Mutex{}, tokens: &fakeTokens{}, reports: &fakeRepoReports{}}
	e.manager = &fakeManager{defs: map[string]supervisor.AgentDefinition{}}
	if created {
		if err := os.MkdirAll(e.folder, 0700); err != nil {
			t.Fatal(err)
		}
		e.manager.defs["agm_1"] = supervisor.AgentDefinition{AgentID: "agm_1", Name: "eng-1", Kind: "started", Desired: "running", State: "running", Mode: "headless", Workstream: "348", WorkFolder: e.folder, Repos: []string{"owner/first"}, Revision: 1}
	}
	e.remote = []Agent{{AgentID: "agm_1", Name: "eng-1", Desired: "running", Mode: "headless", WorkFolder: e.folder, Repos: []string{"owner/first", "Other/second"}, AssignedOrganizationID: "org_a", AssignedWorkstreamCode: "348", Revision: 1}}
	if !created {
		e.remote[0].Repos = []string{"Other/second"}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/agent/v1/machines/me/agents":
			_ = json.NewEncoder(w).Encode(Definitions{Agents: e.remote})
		case strings.HasSuffix(req.URL.Path, "/result"):
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected route %s", req.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	previous := githubCloneBase
	githubCloneBase = "file://" + e.origin + "/"
	t.Cleanup(func() { githubCloneBase = previous })
	e.r = &AgentReconciler{API: HTTPAPI{BaseURL: server.URL, Client: server.Client(), Store: store}, Manager: e.manager, Store: store, Home: home, Gate: e.gate, Tokens: e.tokens, RepoReports: e.reports, Now: func() time.Time { return e.now }, Join: func(context.Context, Agent) error { return nil }}
	return e
}

// publish creates the bare origin repository for owner/name.
func (e *addRepoEnv) publish(t *testing.T, repo string) {
	t.Helper()
	if out, err := exec.Command("git", "init", "--bare", "-q", filepath.Join(e.origin, repo+".git")).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

// pass runs one reconcile and waits for its background clones.
func (e *addRepoEnv) pass(t *testing.T) error {
	t.Helper()
	err := e.r.Reconcile(context.Background())
	e.r.waitRepoJobs()
	return err
}

type addRepoPass struct {
	advance time.Duration
	before  func(*testing.T, *addRepoEnv)
	wantErr bool
}

func notFound() error { return &HTTPStatusError{Path: "/repos", Status: http.StatusNotFound} }

func TestReconcileAddsReposToExistingAgent(t *testing.T) {
	okReport := "agm_1 Other/second ok=true "
	failClone := "agm_1 Other/second ok=false clone Other/second"
	publish := func(t *testing.T, e *addRepoEnv) { e.publish(t, "Other/second") }
	cases := []struct {
		name       string
		passes     []addRepoPass
		wantSaved  bool
		wantClone  bool
		wantTokens int
		wantReport []string
	}{
		{
			name:       "added repo is cloned and saved",
			passes:     []addRepoPass{{before: publish}, {}},
			wantSaved:  true,
			wantClone:  true,
			wantTokens: 1,
			wantReport: []string{okReport},
		},
		{
			name: "existing folder is not re-cloned and counts as ok",
			passes: []addRepoPass{{before: func(t *testing.T, e *addRepoEnv) {
				if err := os.MkdirAll(filepath.Join(e.folder, "second"), 0700); err != nil {
					t.Fatal(err)
				}
			}}},
			wantSaved:  true,
			wantTokens: 1,
			wantReport: []string{okReport},
		},
		{
			name: "failed clone is not saved and is retried after backoff",
			passes: []addRepoPass{
				{},
				// Still backing off: no token renewal and no clone.
				{advance: 10 * time.Second, wantErr: true},
				{advance: 21 * time.Second, before: publish},
			},
			wantSaved:  true,
			wantClone:  true,
			wantTokens: 2,
			wantReport: []string{failClone, okReport},
		},
		{
			name: "each distinct failure is reported once",
			passes: []addRepoPass{
				{before: func(_ *testing.T, e *addRepoEnv) { e.tokens.err = errors.New("token down") }},
				// Each retry waits out a doubling backoff: 30s, 60s, 2m.
				{advance: 31 * time.Second},
				{advance: 61 * time.Second, before: func(_ *testing.T, e *addRepoEnv) { e.tokens.err = nil }},
				{advance: 121 * time.Second},
			},
			wantTokens: 4,
			wantReport: []string{"agm_1 Other/second ok=false renew GitHub token for Other: token down", failClone},
		},
		{
			name: "lost report is resent until acknowledged",
			passes: []addRepoPass{
				{before: func(t *testing.T, e *addRepoEnv) {
					publish(t, e)
					e.reports.err = &HTTPStatusError{Path: "/repos", Status: http.StatusBadGateway}
				}},
				{},
				{before: func(_ *testing.T, e *addRepoEnv) { e.reports.err = nil }},
				{},
			},
			wantSaved:  true,
			wantClone:  true,
			wantTokens: 1,
			// The background flush and the next reconcile fail, the third
			// send is acknowledged, and nothing is sent after that.
			wantReport: []string{okReport, okReport, okReport},
		},
		{
			name: "404 from report stops resending and never blocks cloning",
			passes: []addRepoPass{
				{before: func(t *testing.T, e *addRepoEnv) {
					publish(t, e)
					e.reports.err = notFound()
				}},
				{},
			},
			wantSaved:  true,
			wantClone:  true,
			wantTokens: 1,
			wantReport: []string{okReport},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newAddRepoEnv(t, true)
			for i, p := range tc.passes {
				e.now = e.now.Add(p.advance)
				if p.before != nil {
					p.before(t, e)
				}
				if err := e.pass(t); (err != nil) != p.wantErr {
					t.Fatalf("pass %d: err=%v wantErr=%v", i, err, p.wantErr)
				}
			}
			repos := e.manager.defs["agm_1"].Repos
			if saved := slices.Contains(repos, "Other/second"); saved != tc.wantSaved {
				t.Fatalf("saved=%v repos=%v", saved, repos)
			}
			for _, call := range e.manager.calls {
				if !strings.HasPrefix(call, "add-repo:") {
					t.Fatalf("agent lifecycle touched: %v", e.manager.calls)
				}
			}
			if _, err := os.Stat(filepath.Join(e.folder, "second", ".git", "HEAD")); (err == nil) != tc.wantClone {
				t.Fatalf("clone present=%v want %v", err == nil, tc.wantClone)
			}
			if e.tokens.count() != tc.wantTokens || (tc.wantTokens > 0 && e.tokens.calls[0] != "Other:true") {
				t.Fatalf("token calls=%v want %d forced for Other", e.tokens.calls, tc.wantTokens)
			}
			checkReports(t, e.reports.reports, tc.wantReport)
		})
	}
}

func TestAddRepoClonesOutsideOperationLock(t *testing.T) {
	cases := []struct {
		name         string
		extraPasses  int
		wantTokenHit int
	}{
		{"single pass", 0, 1},
		{"later passes do not start the same clone", 2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newAddRepoEnv(t, true)
			e.publish(t, "Other/second")
			e.tokens.entered, e.tokens.release = make(chan struct{}), make(chan struct{})
			if err := e.r.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			<-e.tokens.entered // the clone job is running now
			if !e.gate.TryLock() {
				t.Fatal("operation lock held while the clone runs")
			}
			e.gate.Unlock()
			for i := 0; i < tc.extraPasses; i++ {
				if err := e.r.Reconcile(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			close(e.tokens.release)
			e.r.waitRepoJobs()
			if e.tokens.count() != tc.wantTokenHit || !slices.Contains(e.manager.defs["agm_1"].Repos, "Other/second") {
				t.Fatalf("tokens=%v repos=%v", e.tokens.calls, e.manager.defs["agm_1"].Repos)
			}
		})
	}
}

func TestCreatedAgentReportsInitialRepos(t *testing.T) {
	cases := []struct {
		name       string
		reportErr  error
		wantReport []string
	}{
		{"acknowledged", nil, []string{"agm_1 Other/second ok=true "}},
		{"untracked repo answers 404", notFound(), []string{"agm_1 Other/second ok=true "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newAddRepoEnv(t, false)
			e.publish(t, "Other/second")
			e.reports.err = tc.reportErr
			for i := 0; i < 2; i++ {
				if err := e.pass(t); err != nil {
					t.Fatal(err)
				}
			}
			if !slices.Equal(e.manager.calls, []string{"start:eng-1"}) {
				t.Fatalf("calls=%v", e.manager.calls)
			}
			if _, err := os.Stat(filepath.Join(e.folder, "second", ".git", "HEAD")); err != nil {
				t.Fatalf("initial clone missing: %v", err)
			}
			checkReports(t, e.reports.reports, tc.wantReport)
		})
	}
}

func TestRepoDelay(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{1, 30 * time.Second},
		{2, time.Minute},
		{3, 2 * time.Minute},
		{5, 8 * time.Minute},
		{6, 10 * time.Minute},
		{40, 10 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.failures), func(t *testing.T) {
			if got := repoDelay(tc.failures); got != tc.want {
				t.Fatalf("repoDelay(%d)=%v want %v", tc.failures, got, tc.want)
			}
		})
	}
}

// checkReports matches each report by prefix; git's wording is not fixed.
func checkReports(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("reports=%q want %q", got, want)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Fatalf("report %d=%q want prefix %q", i, got[i], want[i])
		}
	}
}

func TestAddedRepos(t *testing.T) {
	cases := []struct {
		name          string
		saved, server []string
		want          []string
	}{
		{"none added", []string{"a/b"}, []string{"a/b"}, nil},
		{"one added", []string{"a/b"}, []string{"a/b", "c/d"}, []string{"c/d"}},
		{"case differs", []string{"A/B"}, []string{"a/b"}, nil},
		{"removed on server is ignored", []string{"a/b", "c/d"}, []string{"a/b"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := addedRepos(tc.saved, tc.server); !slices.Equal(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestIsNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"404", notFound(), true},
		{"wrapped 404", fmt.Errorf("report repository x: %w", notFound()), true},
		{"502", &HTTPStatusError{Path: "/repos", Status: http.StatusBadGateway}, false},
		{"transport", errors.New("connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNotFound(tc.err); got != tc.want {
				t.Fatalf("isNotFound=%v want %v", got, tc.want)
			}
		})
	}
}
