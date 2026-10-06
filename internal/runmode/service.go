package runmode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Stopper holds the supervisor's launch gate before stopping processes.
type Stopper interface{ BeginRunFinishing(context.Context) error }
type Service struct {
	Home     string
	API      API
	Tokens   *TokenSource
	Stop     Stopper
	Clones   func() []Clone
	Interval time.Duration
	Now      func() time.Time
	OnError  func(error)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
func (s *Service) error(err error) {
	if err != nil && s.OnError != nil {
		s.OnError(err)
	}
}

// Run checks the server's authoritative state, not an untrusted socket frame.
// A failed finishing step is retried until rescueDeadline; the server destroys
// the machine even if this daemon never finishes.
func (s *Service) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var done bool
	check := func() {
		if done {
			return
		}
		status, err := s.API.Status(ctx)
		if err != nil {
			s.error(err)
			return
		}
		if status.State != "finishing" {
			return
		}
		if err = s.Finish(ctx, status); err != nil {
			s.error(err)
			return
		}
		done = true
	}
	check()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}
func (s *Service) Finish(ctx context.Context, status RunStatus) error {
	if status.State != "finishing" || status.RunID == "" || s.Stop == nil || s.Tokens == nil || s.Clones == nil {
		return errors.New("run finishing is not configured")
	}
	deadline := s.now().Add(5 * time.Minute)
	if status.RescueDeadline != nil && status.RescueDeadline.Before(deadline) {
		deadline = *status.RescueDeadline
	}
	if !deadline.After(s.now()) {
		return errors.New("run rescue deadline has elapsed")
	}
	limited, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	// Fetch before stopping, because Finishing is the last state allowed to mint.
	if _, err := s.Tokens.Get(limited, true); err != nil {
		return fmt.Errorf("renew GitHub token for rescue: %w", err)
	}
	if err := s.Stop.BeginRunFinishing(limited); err != nil {
		return err
	}
	results := (Rescuer{RunID: status.RunID, Clones: s.Clones()}).Rescue(limited)
	uploaded := false
	path := filepath.Join(s.Home, ".pi", "agent", "auth.json")
	if data, err := os.ReadFile(path); err == nil {
		var auth map[string]json.RawMessage
		if json.Unmarshal(data, &auth) == nil && len(auth["openai-codex"]) != 0 {
			login, _ := json.Marshal(map[string]json.RawMessage{"openai-codex": auth["openai-codex"]})
			if e := s.API.UploadLogin(limited, login); e == nil {
				uploaded = true
			} else {
				s.error(e)
			}
		}
	}
	// A failed upload is visible via loginUploaded=false and must not prevent
	// reporting rescue results before the server's five-minute destroy timer.
	if err := s.API.Finished(limited, results, uploaded); err != nil {
		return err
	}
	return nil
}

// WatchLogin uploads only the selected provider entry. Stale/account-mismatch
// writes are rejected server-side; this watcher never overwrites the source.
func (s *Service) WatchLogin(ctx context.Context) {
	path := filepath.Join(s.Home, ".pi", "agent", "auth.json")
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var last string
	if data, err := os.ReadFile(path); err == nil {
		var initial map[string]json.RawMessage
		if json.Unmarshal(data, &initial) == nil {
			last = strings.TrimSpace(string(initial["openai-codex"]))
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var auth map[string]json.RawMessage
			if json.Unmarshal(data, &auth) != nil || len(auth["openai-codex"]) == 0 {
				continue
			}
			entry := strings.TrimSpace(string(auth["openai-codex"]))
			if entry == last {
				continue
			}
			login, _ := json.Marshal(map[string]json.RawMessage{"openai-codex": auth["openai-codex"]})
			if err = s.API.UploadLogin(ctx, login); err != nil {
				s.error(err)
				continue
			}
			last = entry
		}
	}
}
