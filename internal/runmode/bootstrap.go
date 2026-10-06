package runmode

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

// Bootstrap creates a run's device login without an interactive human login.
// Client and MetadataURL are injectable so the exchange and IMDSv2 handshake
// can be tested without a cloud instance. No start code is passed on argv.
type Bootstrap struct {
	Home, BaseURL, MetadataURL string
	Client                     *http.Client
	Random                     io.Reader
	Now                        func() time.Time
}

type Run struct {
	RunID          string    `json:"runId"`
	OrganizationID string    `json:"organizationId"`
	WorkstreamCode string    `json:"workstreamCode"`
	HardLimitAt    time.Time `json:"hardLimitAt"`
}

type bootstrapReply struct {
	RunID    string `json:"runId"`
	DeviceID string `json:"deviceId"`
	Machine  struct {
		Version             int    `json:"version"`
		DeviceID            string `json:"deviceId"`
		MachineName         string `json:"machineName"`
		MachineSocketSecret string `json:"machineSocketSecret"`
		CreatedAt           string `json:"createdAt"`
	} `json:"machine"`
	PiAuth         json.RawMessage `json:"piAuth"`
	PiModels       json.RawMessage `json:"piModels"`
	PiSettings     json.RawMessage `json:"piSettings"`
	HardLimitAt    time.Time       `json:"hardLimitAt"`
	WorkstreamCode string          `json:"workstreamCode"`
	OrganizationID string          `json:"organizationId"`
}

type pending struct {
	RunID string `json:"runId"`
	Key   string `json:"key"`
	Token string `json:"token"`
}

func (b Bootstrap) client() *http.Client {
	if b.Client != nil {
		return b.Client
	}
	return &http.Client{Timeout: 20 * time.Second}
}
func (b Bootstrap) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}
func (b Bootstrap) random() io.Reader {
	if b.Random != nil {
		return b.Random
	}
	return rand.Reader
}

// Exchange reads the protected start-code file, proves possession of a fresh
// key and the AWS instance identity, then installs the returned run kit.
func (b Bootstrap) Exchange(ctx context.Context, codeFile string) (Run, error) {
	if b.Home == "" || b.BaseURL == "" || codeFile == "" {
		return Run{}, errors.New("bootstrap is not configured")
	}
	info, err := os.Lstat(codeFile)
	if err != nil {
		return Run{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return Run{}, errors.New("start code file must be owner-only and regular")
	}
	code, err := os.ReadFile(codeFile)
	if err != nil {
		return Run{}, err
	}
	parts := strings.SplitN(strings.TrimSpace(string(code)), ".", 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "run_") || len(parts[1]) < 32 {
		return Run{}, errors.New("invalid start code")
	}
	runID, secret := parts[0], parts[1]
	// Do not log the code or include it in errors. The server compares the hash
	// and validates the signed run/secret/timestamp proof.
	hash := sha256.Sum256([]byte(secret))
	secretHash := hex.EncodeToString(hash[:])
	state, err := b.pending(runID)
	if err != nil {
		return Run{}, err
	}
	private, err := base64.StdEncoding.DecodeString(state.Key)
	if err != nil || len(private) != ed25519.PrivateKeySize {
		return Run{}, errors.New("invalid pending bootstrap key")
	}
	document, signature, err := b.identity(ctx)
	if err != nil {
		return Run{}, fmt.Errorf("instance identity unavailable: %w", err)
	}
	timestamp := b.now().UTC().Format(time.RFC3339)
	proof := ed25519.Sign(ed25519.PrivateKey(private), []byte(runID+"|"+secretHash+"|"+timestamp))
	payload := map[string]any{"runId": runID, "secretHash": secretHash, "secret": secret, "publicKey": base64.StdEncoding.EncodeToString(ed25519.PrivateKey(private).Public().(ed25519.PublicKey)), "proof": base64.StdEncoding.EncodeToString(proof), "timestamp": timestamp, "apiToken": state.Token, "identity": map[string]string{"document": document, "signature": signature}}
	data, err := json.Marshal(payload)
	if err != nil {
		return Run{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(b.BaseURL, "/")+"/machine/v1/runs/bootstrap", bytes.NewReader(data))
	if err != nil {
		return Run{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := b.client().Do(req)
	if err != nil {
		return Run{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooEarly {
		return Run{}, errors.New("run instance is not ready; retry bootstrap with the same code file")
	}
	if response.StatusCode != http.StatusOK {
		return Run{}, fmt.Errorf("bootstrap rejected (HTTP %d)", response.StatusCode)
	}
	var result bootstrapReply
	if json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&result) != nil || result.RunID != runID || result.DeviceID == "" || result.DeviceID != result.Machine.DeviceID || result.Machine.MachineSocketSecret == "" || result.WorkstreamCode == "" || result.OrganizationID == "" || result.HardLimitAt.Before(b.now()) || len(result.PiAuth) == 0 || len(result.PiModels) == 0 || len(result.PiSettings) == 0 {
		return Run{}, errors.New("invalid bootstrap response")
	}
	var auth map[string]json.RawMessage
	if json.Unmarshal(result.PiAuth, &auth) != nil || len(auth) != 1 || len(auth["openai-codex"]) == 0 {
		return Run{}, errors.New("bootstrap response includes invalid model login")
	}
	run := Run{RunID: runID, OrganizationID: result.OrganizationID, WorkstreamCode: result.WorkstreamCode, HardLimitAt: result.HardLimitAt}
	if err = b.install(run, result, state.Token); err != nil {
		return Run{}, err
	}
	_ = os.Remove(filepath.Join(storagepath.Root(b.Home), "bootstrap-pending.json"))
	return run, nil
}

func (b Bootstrap) pending(runID string) (pending, error) {
	path := filepath.Join(storagepath.Root(b.Home), "bootstrap-pending.json")
	if data, err := os.ReadFile(path); err == nil {
		var p pending
		if json.Unmarshal(data, &p) != nil || p.RunID != runID || p.Token == "" || p.Key == "" {
			return p, errors.New("pending bootstrap belongs to another run")
		}
		return p, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return pending{}, err
	}
	_, key, err := ed25519.GenerateKey(b.random())
	if err != nil {
		return pending{}, err
	}
	token := make([]byte, 32)
	if _, err = io.ReadFull(b.random(), token); err != nil {
		return pending{}, err
	}
	p := pending{RunID: runID, Key: base64.StdEncoding.EncodeToString(key), Token: base64.RawURLEncoding.EncodeToString(token)}
	data, _ := json.Marshal(p)
	if err = writePrivate(path, data); err != nil {
		return pending{}, err
	}
	return p, nil
}

func (b Bootstrap) identity(ctx context.Context) (string, string, error) {
	base := b.MetadataURL
	if base == "" {
		base = "http://169.254.169.254"
	}
	base = strings.TrimRight(base, "/")
	tokenReq, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/latest/api/token", nil)
	if err != nil {
		return "", "", err
	}
	tokenReq.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "21600")
	response, err := b.client().Do(tokenReq)
	if err != nil {
		return "", "", err
	}
	token, err := readMetadata(response)
	if err != nil {
		return "", "", err
	}
	get := func(path string) (string, error) {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if e != nil {
			return "", e
		}
		req.Header.Set("X-aws-ec2-metadata-token", token)
		r, e := b.client().Do(req)
		if e != nil {
			return "", e
		}
		return readMetadata(r)
	}
	doc, err := get("/latest/dynamic/instance-identity/document")
	if err != nil {
		return "", "", err
	}
	sig, err := get("/latest/dynamic/instance-identity/rsa2048")
	return doc, sig, err
}
func readMetadata(r *http.Response) (string, error) {
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata HTTP %d", r.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", errors.New("empty metadata response")
	}
	return value, nil
}

func (b Bootstrap) install(run Run, r bootstrapReply, token string) error {
	root := storagepath.Root(b.Home)
	pi := filepath.Join(b.Home, ".pi", "agent")
	for _, file := range []struct {
		path string
		data []byte
	}{{filepath.Join(pi, "auth.json"), r.PiAuth}, {filepath.Join(pi, "models.json"), r.PiModels}, {filepath.Join(pi, "settings.json"), r.PiSettings}} {
		if err := writePrivate(file.path, file.data); err != nil {
			return err
		}
	}
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	if err = writePrivate(filepath.Join(root, "run.json"), data); err != nil {
		return err
	}
	return credentials.NewStore(b.Home).SaveMachine(credentials.Machine{APIToken: token, DeviceID: r.DeviceID, MachineName: r.Machine.MachineName, MachineSocketSecret: r.Machine.MachineSocketSecret, CreatedAt: r.Machine.CreatedAt})
}

func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".aircom-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0o600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func Load(home string) (Run, error) {
	var run Run
	data, err := os.ReadFile(filepath.Join(storagepath.Root(home), "run.json"))
	if err != nil {
		return run, err
	}
	err = json.Unmarshal(data, &run)
	if err != nil || run.RunID == "" || run.HardLimitAt.IsZero() {
		return Run{}, errors.New("invalid run mode file")
	}
	return run, nil
}
