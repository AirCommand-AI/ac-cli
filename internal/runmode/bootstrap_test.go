package runmode

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func TestBootstrapUsesIMDSv2ProofAndOwnerOnlyFiles(t *testing.T) {
	home := t.TempDir()
	runID := "run_0123456789abcdef01234567"
	secret := base64.RawURLEncoding.EncodeToString([]byte("thirty-two-byte-start-code-value!"))
	hash := sha256.Sum256([]byte(secret))
	code := filepath.Join(home, "code")
	if err := os.WriteFile(code, []byte(runID+"."+secret), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var originalKey, originalToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest/api/token":
			if r.Method != "PUT" || r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") != "21600" {
				t.Errorf("IMDS token: %s %+v", r.Method, r.Header)
			}
			_, _ = w.Write([]byte("imds-token"))
		case "/latest/dynamic/instance-identity/document", "/latest/dynamic/instance-identity/rsa2048":
			if r.Header.Get("X-aws-ec2-metadata-token") != "imds-token" {
				t.Error("missing IMDS token")
			}
			if strings.HasSuffix(r.URL.Path, "document") {
				_, _ = w.Write([]byte(`{"instanceId":"i-example"}`))
			} else {
				_, _ = w.Write([]byte("pkcs7-base64"))
			}
		case "/machine/v1/runs/bootstrap":
			calls++
			var req struct {
				RunID, Secret, SecretHash, PublicKey, Proof, Timestamp, APIToken string
				Identity                                                         struct{ Document, Signature string }
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				t.Error("invalid request")
			}
			if req.RunID != runID || req.Secret != secret || req.SecretHash != hex.EncodeToString(hash[:]) || req.Identity.Signature != "pkcs7-base64" {
				t.Errorf("invalid exchange: %+v", req)
			}
			key, _ := base64.StdEncoding.DecodeString(req.PublicKey)
			proof, _ := base64.StdEncoding.DecodeString(req.Proof)
			if !ed25519.Verify(key, []byte(runID+"|"+req.SecretHash+"|"+req.Timestamp), proof) {
				t.Error("bad signed proof")
			}
			if calls == 1 {
				originalKey, originalToken = req.PublicKey, req.APIToken
				w.WriteHeader(425)
				return
			}
			if req.PublicKey != originalKey || req.APIToken != originalToken {
				t.Error("retry changed pending key/token")
			}
			_, _ = w.Write([]byte(`{"runId":"` + runID + `","deviceId":"dev_test","machine":{"version":1,"deviceId":"dev_test","machineName":"run-test","machineSocketSecret":"socket-key","createdAt":"2026-10-06T00:00:00Z"},"piAuth":{"openai-codex":{"type":"oauth","access":"access"}},"piModels":{"providers":{"openai-codex":{}}},"piSettings":{"defaultProvider":"openai-codex"},"hardLimitAt":"2030-10-06T00:00:00Z","workstreamCode":"107","organizationId":"org_test"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	b := Bootstrap{Home: home, BaseURL: server.URL, MetadataURL: server.URL, Client: server.Client(), Now: func() time.Time { return time.Date(2026, 10, 6, 16, 0, 0, 0, time.UTC) }}
	if _, err := b.Exchange(context.Background(), code); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("first exchange: %v", err)
	}
	run, err := b.Exchange(context.Background(), code)
	if err != nil {
		t.Fatal(err)
	}
	if run.RunID != runID || run.OrganizationID != "org_test" {
		t.Fatal(run)
	}
	machine, err := credentials.NewStore(home).LoadMachine()
	if err != nil || machine.APIToken != originalToken || machine.MachineSocketSecret != "socket-key" {
		t.Fatalf("machine: %+v %v", machine, err)
	}
	for _, path := range []string{filepath.Join(home, ".aircommand", "machine.json"), filepath.Join(home, ".aircommand", "run.json"), filepath.Join(home, ".pi", "agent", "auth.json"), filepath.Join(home, ".pi", "agent", "models.json"), filepath.Join(home, ".pi", "agent", "settings.json")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private %s: %v %+v", path, err, info)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".aircommand", "bootstrap-pending.json")); !os.IsNotExist(err) {
		t.Fatalf("pending key not removed: %v", err)
	}
}
func TestBootstrapRejectsExposedStartCode(t *testing.T) {
	home := t.TempDir()
	code := filepath.Join(home, "code")
	_ = os.WriteFile(code, []byte("run_0123456789abcdef01234567.longsecretlongsecretlongsecretlongsecret"), 0o644)
	_, err := (Bootstrap{Home: home, BaseURL: "https://example.invalid"}).Exchange(context.Background(), code)
	if err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("insecure code accepted: %v", err)
	}
}
