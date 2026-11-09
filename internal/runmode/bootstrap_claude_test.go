package runmode

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/claudeauth"
)

func bootstrapWith(t *testing.T, models string) (string, error) {
	t.Helper()
	home := t.TempDir()
	runID := "run_0123456789abcdef01234567"
	raw := []byte("thirty-two-byte-start-code-value")
	_ = sha256.Sum256(raw)
	code := filepath.Join(home, "code")
	if err := os.WriteFile(code, []byte(runID+"."+base64.RawURLEncoding.EncodeToString(raw)), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/latest/api/token":
			_, _ = w.Write([]byte("imds-token"))
		case strings.HasSuffix(r.URL.Path, "document"):
			_, _ = w.Write([]byte(`{"instanceId":"i-example"}`))
		case strings.HasSuffix(r.URL.Path, "rsa2048"):
			_, _ = w.Write([]byte("pkcs7-base64"))
		case r.URL.Path == "/machine/v1/runs/bootstrap":
			_, _ = w.Write([]byte(`{"runId":"` + runID + `","deviceId":"dev_test","machine":{"version":1,"deviceId":"dev_test","machineName":"run-test","machineSocketSecret":"socket-key","createdAt":"2026-10-06T00:00:00Z"},` + models + `"hardLimitAt":"2030-10-06T00:00:00Z","workstreamCode":"107","organizationId":"org_test"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	b := Bootstrap{Home: home, BaseURL: server.URL, MetadataURL: server.URL, Client: server.Client(), Now: func() time.Time { return time.Date(2026, 10, 6, 16, 0, 0, 0, time.UTC) }}
	_, err := b.Exchange(context.Background(), code)
	return home, err
}

func TestBootstrapClaudeOnly(t *testing.T) {
	home, err := bootstrapWith(t, `"claude":{"oauthToken":"sk-ant-oat01-token","expiresAt":"2027-11-09T00:00:00Z","baseUrl":"https://api.multi-router.ai","routerId":"rt_claude","apiTokenHeader":"Bearer mr"},`)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := claudeauth.Load(home)
	if err != nil || auth.RouterID != "rt_claude" {
		t.Fatalf("claude auth = %+v %v", auth, err)
	}
	info, err := os.Stat(claudeauth.Path(home))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("claude.json mode: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".pi", "agent", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("pi login written without pi agents: %v", err)
	}
}

func TestBootstrapNeedsAModelLogin(t *testing.T) {
	cases := map[string]string{
		"none":           ``,
		"invalid claude": `"claude":{"oauthToken":"sk-ant-api03-key","baseUrl":"https://x","routerId":"r","apiTokenHeader":"h"},`,
	}
	for name, models := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := bootstrapWith(t, models); err == nil {
				t.Fatal("bootstrap accepted")
			}
		})
	}
}
