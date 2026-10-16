package runmode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// passRenewBefore: fetch a new pass once the current one has less than
	// this left. The dashboard renews the login itself below 30 minutes.
	passRenewBefore = 35 * time.Minute
	// managedRefresh fills pi's refresh field. pi only renews an expired
	// pass, and KeepLogin replaces passes well before they expire.
	managedRefresh = "aircommand-managed"
)

// KeepLogin keeps pi's auth.json holding a valid ChatGPT pass from the
// dashboard. pi re-reads auth.json each time it needs the login, so a new
// pass takes effect on the agents' next model call. When the login needs
// sign-in it keeps asking every interval, so agents resume within a minute
// of someone signing in again.
func (s *Service) KeepLogin(ctx context.Context) {
	interval := s.LoginInterval
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	signIn := false
	for {
		err := s.refreshPass(ctx)
		switch {
		case errors.Is(err, ErrLoginNeedsSignIn):
			if !signIn {
				s.error(fmt.Errorf("agents paused: %w", err))
			}
			signIn = true
		case err != nil:
			s.error(fmt.Errorf("ChatGPT pass: %w", err))
		default:
			if signIn {
				s.error(errors.New("ChatGPT login signed in again; agents resume"))
			}
			signIn = false
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) authPath() string { return filepath.Join(s.Home, ".pi", "agent", "auth.json") }

// refreshPass fetches a pass when the installed one is missing or expiring.
func (s *Service) refreshPass(ctx context.Context) error {
	auth := map[string]json.RawMessage{}
	if data, err := os.ReadFile(s.authPath()); err == nil {
		_ = json.Unmarshal(data, &auth)
	}
	var current ChatGPTPass
	_ = json.Unmarshal(auth["openai-codex"], &current)
	if current.Access != "" && time.UnixMilli(current.Expires).After(s.now().Add(passRenewBefore)) {
		return nil
	}
	pass, err := s.API.ChatGPTPass(ctx)
	if err != nil {
		return err
	}
	if pass.Access == current.Access {
		return nil
	}
	entry, err := json.Marshal(map[string]any{"type": "oauth", "access": pass.Access, "refresh": managedRefresh, "expires": pass.Expires, "accountId": pass.AccountID})
	if err != nil {
		return err
	}
	auth["openai-codex"] = entry
	data, err := json.Marshal(auth)
	if err != nil {
		return err
	}
	// writePrivate renames into place, so pi never reads a partial file.
	return writePrivate(s.authPath(), data)
}
