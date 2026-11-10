package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, output, err)
	}
	return strings.TrimSpace(string(output))
}
func TestTaskCommitComputesGitNumstatAndTestsAreReported(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "config", "user.name", "A Tester")
	gitTest(t, dir, "config", "user.email", "a@example.com")
	gitTest(t, dir, "remote", "add", "origin", "git@github.com:Acme/widget.git")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("first\nsecond\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "file.txt")
	gitTest(t, dir, "commit", "-m", "first")
	sha := gitTest(t, dir, "rev-parse", "HEAD")
	var commits []commitEvidence
	var tests map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(structuredDetail))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/commits") {
			var item commitEvidence
			if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
				t.Error(err)
			}
			commits = append(commits, item)
		} else if strings.HasSuffix(r.URL.Path, "/tests") {
			if err := json.NewDecoder(r.Body).Decode(&tests); err != nil {
				t.Error(err)
			}
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, _, stderr := testApp(t, server.URL, "", deterministicRandom(0x5a))
	saveTestCredential(t, client, testCredential())
	if client.Run([]string{"task", "5", "--workstream", "694", "--commit", sha, "--repo", dir}) != 0 {
		t.Fatal(stderr.String())
	}
	if len(commits) != 1 || commits[0].SHA != sha || commits[0].Repo != "Acme/widget" || commits[0].Additions != 2 || commits[0].Deletions != 0 || commits[0].FilesChanged != 1 || commits[0].Files[0] != "file.txt" {
		t.Fatalf("commit = %+v", commits)
	}
	if client.Run([]string{"task", "5", "--workstream", "694", "--commit", "deadbeef", "--repo", dir}) == 0 || len(commits) != 1 {
		t.Fatal("unknown commit accepted")
	}
	if client.Run([]string{"task", "5", "--workstream", "694", "--tests", "9/1/2", "--suite", "unit"}) != 0 {
		t.Fatal(stderr.String())
	}
	if tests["passed"] != float64(9) || tests["failed"] != float64(1) || tests["skipped"] != float64(2) || tests["suite"] != "unit" {
		t.Fatalf("tests = %+v", tests)
	}
}

func TestTaskCommitRepoAcceptsOwnerRepo(t *testing.T) {
	work := t.TempDir()
	repo := filepath.Join(work, "widget")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "init")
	gitTest(t, repo, "remote", "add", "origin", "https://github.com/Acme/widget.git")
	other := filepath.Join(work, "other")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, cwd, arg, want string
		ok                   bool
	}{
		{"inside the checkout", repo, "Acme/widget", repo, true},
		{"next to the checkout", work, "Acme/widget", repo, true},
		{"sibling directory", other, "Acme/widget", repo, true},
		{"case of owner differs", repo, "acme/Widget", repo, true},
		{"a path still works", work, "widget", repo, true},
		{"no matching checkout", other, "Acme/missing", "", false},
		{"origin is another repo", repo, "Acme/gadget", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(tc.cwd)
			got, err := resolveRepoDir(tc.arg)
			if (err == nil) != tc.ok {
				t.Fatalf("err %v", err)
			}
			if tc.ok {
				want, _ := filepath.EvalSymlinks(tc.want)
				have, _ := filepath.EvalSymlinks(got)
				if have != want {
					t.Fatalf("got %s want %s", got, tc.want)
				}
			}
		})
	}
}
