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
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

type fakeTokens struct {
	calls []string
	err   error
}

func (f *fakeTokens) For(_ context.Context, owner string, force bool) (string, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s:%v", owner, force))
	return "tok", f.err
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
	manager              *fakeManager
	tokens               *fakeTokens
	reports              *fakeRepoReports
	r                    *AgentReconciler
}

func newAddRepoEnv(t *testing.T) *addRepoEnv {
	t.Helper()
	home := t.TempDir()
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{APIToken: "device", DeviceID: "dev_1"}); err != nil {
		t.Fatal(err)
	}
	e := &addRepoEnv{home: home, folder: filepath.Join(home, "work", "eng-1"), origin: t.TempDir(), tokens: &fakeTokens{}, reports: &fakeRepoReports{}}
	if err := os.MkdirAll(e.folder, 0700); err != nil {
		t.Fatal(err)
	}
	e.manager = &fakeManager{defs: map[string]supervisor.AgentDefinition{"agm_1": {AgentID: "agm_1", Name: "eng-1", Kind: "started", Desired: "running", State: "running", Mode: "headless", Workstream: "348", WorkFolder: e.folder, Repos: []string{"owner/first"}, Revision: 1}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/agent/v1/machines/me/agents" {
			t.Errorf("unexpected route %s", req.URL.Path)
			return
		}
		// The dashboard adds repositories without bumping the revision.
		_ = json.NewEncoder(w).Encode(Definitions{Agents: []Agent{{AgentID: "agm_1", Name: "eng-1", Desired: "running", Mode: "headless", WorkFolder: e.folder, Repos: []string{"owner/first", "Other/second"}, AssignedOrganizationID: "org_a", AssignedWorkstreamCode: "348", Revision: 1}}})
	}))
	t.Cleanup(server.Close)
	previous := githubCloneBase
	githubCloneBase = "file://" + e.origin + "/"
	t.Cleanup(func() { githubCloneBase = previous })
	e.r = &AgentReconciler{API: HTTPAPI{BaseURL: server.URL, Client: server.Client(), Store: store}, Manager: e.manager, Store: store, Home: home, Tokens: e.tokens, RepoReports: e.reports}
	return e
}

// publish creates the bare origin repository for owner/name.
func (e *addRepoEnv) publish(t *testing.T, repo string) {
	t.Helper()
	if out, err := exec.Command("git", "init", "--bare", "-q", filepath.Join(e.origin, repo+".git")).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

type addRepoPass struct {
	before  func(*testing.T, *addRepoEnv)
	wantErr bool
}

func TestReconcileAddsReposToExistingAgent(t *testing.T) {
	okReport := "agm_1 Other/second ok=true "
	cases := []struct {
		name       string
		passes     []addRepoPass
		wantSaved  bool
		wantClone  bool
		wantTokens []string
		wantReport []string
	}{
		{
			name:       "added repo is cloned and saved",
			passes:     []addRepoPass{{before: func(t *testing.T, e *addRepoEnv) { e.publish(t, "Other/second") }}},
			wantSaved:  true,
			wantClone:  true,
			wantTokens: []string{"Other:true"},
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
			wantTokens: []string{"Other:true"},
			wantReport: []string{okReport},
		},
		{
			name: "failed clone is not saved and is retried",
			passes: []addRepoPass{
				{wantErr: true},
				{wantErr: true},
				{before: func(t *testing.T, e *addRepoEnv) { e.publish(t, "Other/second") }},
			},
			wantSaved:  true,
			wantClone:  true,
			wantTokens: []string{"Other:true", "Other:true", "Other:true"},
			wantReport: []string{"agm_1 Other/second ok=false clone Other/second", okReport},
		},
		{
			name: "each distinct failure is reported once",
			passes: []addRepoPass{
				{before: func(_ *testing.T, e *addRepoEnv) { e.tokens.err = errors.New("token down") }, wantErr: true},
				{wantErr: true},
				{before: func(_ *testing.T, e *addRepoEnv) { e.tokens.err = nil }, wantErr: true},
				{wantErr: true},
			},
			wantTokens: []string{"Other:true", "Other:true", "Other:true", "Other:true"},
			wantReport: []string{"agm_1 Other/second ok=false renew GitHub token for Other: token down", "agm_1 Other/second ok=false clone Other/second"},
		},
		{
			name: "report error never blocks cloning",
			passes: []addRepoPass{{before: func(t *testing.T, e *addRepoEnv) {
				e.publish(t, "Other/second")
				e.reports.err = &HTTPStatusError{Path: "/repos", Status: http.StatusNotFound}
			}}},
			wantSaved:  true,
			wantClone:  true,
			wantTokens: []string{"Other:true"},
			wantReport: []string{okReport},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newAddRepoEnv(t)
			for i, pass := range tc.passes {
				if pass.before != nil {
					pass.before(t, e)
				}
				if err := e.r.Reconcile(context.Background()); (err != nil) != pass.wantErr {
					t.Fatalf("pass %d: err=%v wantErr=%v", i, err, pass.wantErr)
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
			if !slices.Equal(e.tokens.calls, tc.wantTokens) {
				t.Fatalf("token calls=%v want %v", e.tokens.calls, tc.wantTokens)
			}
			checkReports(t, e.reports.reports, tc.wantReport)
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
