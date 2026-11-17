package runmode

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func TestOwnerFromRepoRef(t *testing.T) {
	tests := []struct{ ref, want string }{
		{"deepmesa/bm-api", "deepmesa"},
		{"github.com/deepmesa/bm-api", "deepmesa"},
		{"https://github.com/paceworks-ai/app.git", "paceworks-ai"},
		{"https://github.com/paceworks-ai/app/pull/12", "paceworks-ai"},
		{"git@github.com:Builder-Magic/site.git", "Builder-Magic"},
		{"ssh://git@github.com/deepmesa/x.git", "deepmesa"},
		{"gitlab.com/deepmesa/x", ""},
		{"https://example.com/deepmesa/x", ""},
		{"github.company.com/a/b", ""},
		{"bm-api", ""},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.ref, func(t *testing.T) {
			if got := OwnerFromRepoRef(tc.ref); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestGHOwner(t *testing.T) {
	remote := func() string { return "from-remote" }
	tests := []struct {
		name   string
		args   []string
		ghRepo string
		want   string
	}{
		{"repo flag", []string{"pr", "list", "-R", "deepmesa/x"}, "", "deepmesa"},
		{"long repo flag", []string{"pr", "create", "--repo", "paceworks-ai/y", "--fill"}, "", "paceworks-ai"},
		{"repo equals", []string{"issue", "list", "--repo=Builder-Magic/z"}, "", "Builder-Magic"},
		{"joined short flag", []string{"pr", "view", "-Rdeepmesa/x"}, "", "deepmesa"},
		{"url argument", []string{"pr", "view", "https://github.com/deepmesa/x/pull/3"}, "", "deepmesa"},
		{"api path", []string{"api", "-X", "POST", "repos/paceworks-ai/y/pulls"}, "", "paceworks-ai"},
		{"api leading slash", []string{"api", "/repos/deepmesa/x/issues"}, "", "deepmesa"},
		{"repo clone", []string{"repo", "clone", "deepmesa/x"}, "", "deepmesa"},
		{"GH_REPO", []string{"pr", "list"}, "paceworks-ai/y", "paceworks-ai"},
		{"origin remote", []string{"pr", "create", "--fill"}, "", "from-remote"},
		{"flag beats GH_REPO", []string{"pr", "list", "-R", "deepmesa/x"}, "paceworks-ai/y", "deepmesa"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := GHOwner(tc.args, tc.ghRepo, remote); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestInstallGHShimFirstOnPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", "/usr/bin:/bin")
	if err := InstallGHShim(home, "relative/aircom"); err == nil {
		t.Fatal("relative aircom path accepted")
	}
	for i := 0; i < 2; i++ {
		if err := InstallGHShim(home, "/opt/it's/aircom"); err != nil {
			t.Fatal(err)
		}
	}
	dir := GHShimDirectory(home)
	if got := os.Getenv("PATH"); got != dir+":/usr/bin:/bin" {
		t.Fatalf("PATH %q", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "gh"))
	info, statErr := os.Stat(filepath.Join(dir, "gh"))
	if err != nil || statErr != nil || info.Mode().Perm() != 0o700 || !strings.Contains(string(data), `exec '/opt/it'\''s/aircom' gh "$@"`) {
		t.Fatalf("shim %q %v %v", data, err, info)
	}
	if _, err := realGH(dir); err != nil && !strings.Contains(err.Error(), "not installed") {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if _, err := realGH(dir); err == nil {
		t.Fatal("shim chosen as the real gh")
	}
}

func TestTokensPerOwnerThroughCredentialHelper(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "ac-o-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	store := credentials.NewStore(home)
	if err := store.SaveMachine(credentials.Machine{DeviceID: "dev", APIToken: "machine"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = fmt.Fprintf(w, `{"token":"pace-%d","expiresAt":"2030-10-06T20:00:00Z","tokens":[{"owner":"paceworks-ai","token":"pace-%d","expiresAt":"2030-10-06T20:00:00Z"},{"owner":"deepmesa","token":"deep-%d","expiresAt":"2030-10-06T19:00:00Z"}]}`, calls, calls, calls)
	}))
	defer server.Close()
	now := time.Date(2026, 10, 6, 16, 0, 0, 0, time.UTC)
	tokens := &TokenSource{API: API{BaseURL: server.URL, Client: server.Client(), Store: store}, Now: func() time.Time { return now }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ServeTokens(ctx, home, tokens) }()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, err := os.Stat(TokenSocket(home)); err == nil {
			break
		}
	}
	tests := []struct{ input, want string }{
		{"protocol=https\nhost=github.com\npath=DeepMesa/bm-api.git\n\n", "password=deep-1"},
		{"protocol=https\nhost=github.com\npath=paceworks-ai/app.git\n\n", "password=pace-1"},
		{"protocol=https\nhost=github.com\npath=stranger/x.git\n\n", "password=pace-1"},
		{"protocol=https\nhost=github.com\n\n", "password=pace-1"},
	}
	for _, tc := range tests {
		var out strings.Builder
		if err := GitCredential(ctx, home, strings.NewReader(tc.input), &out); err != nil || !strings.Contains(out.String(), tc.want) {
			t.Fatalf("%q: %q %v", tc.input, out.String(), err)
		}
	}
	if calls != 1 {
		t.Fatalf("calls %d", calls)
	}
	// The earliest expiry renews every token.
	now = now.Add(2*time.Hour + 56*time.Minute)
	if got, err := tokens.For(ctx, "deepmesa", false); err != nil || got != "deep-2" || calls != 2 {
		t.Fatalf("renewal %q %v calls=%d", got, err, calls)
	}
	if got, err := RequestToken(ctx, home, "deepmesa"); err != nil || got != "deep-2" {
		t.Fatalf("socket %q %v", got, err)
	}
}
