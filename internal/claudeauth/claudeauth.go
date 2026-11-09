// Package claudeauth stores the run machine's Claude credentials: the
// operator's subscription token and the Claude Multi-Router router, written
// at bootstrap and refreshed from the dashboard. Never logged.
package claudeauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

// Auth is what Claude Code needs on this machine.
type Auth struct {
	OAuthToken     string `json:"oauthToken"`
	ExpiresAt      string `json:"expiresAt,omitempty"`
	BaseURL        string `json:"baseUrl"`
	RouterID       string `json:"routerId"`
	APITokenHeader string `json:"apiTokenHeader"`
}

// Valid reports whether every field Claude Code needs is present and safe
// to put in an environment variable.
func (a *Auth) Valid() bool {
	return a != nil && strings.HasPrefix(a.OAuthToken, "sk-ant-oat01-") && strings.HasPrefix(a.BaseURL, "https://") && a.RouterID != "" && a.APITokenHeader != "" && !strings.ContainsAny(a.OAuthToken+a.BaseURL+a.RouterID+a.APITokenHeader, "\r\n")
}

// Path is the credentials file.
func Path(home string) string { return filepath.Join(storagepath.Root(home), "claude.json") }

// ErrMissing: this machine has no Claude credentials.
var ErrMissing = errors.New("no Claude credentials on this machine")

// Load reads the credentials.
func Load(home string) (*Auth, error) {
	data, err := os.ReadFile(Path(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrMissing
	}
	if err != nil {
		return nil, err
	}
	var a Auth
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("decode Claude credentials: %w", err)
	}
	if !a.Valid() {
		return nil, ErrMissing
	}
	return &a, nil
}

// Save writes the credentials owner-only, atomically.
func Save(home string, a Auth) error {
	if !a.Valid() {
		return errors.New("invalid Claude credentials")
	}
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	path := Path(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".claude-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
