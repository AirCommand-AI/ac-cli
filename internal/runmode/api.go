package runmode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

type API struct {
	BaseURL string
	Client  *http.Client
	Store   *credentials.Store
}
type RunStatus struct {
	RunID          string     `json:"runId"`
	State          string     `json:"state"`
	HardLimitAt    time.Time  `json:"hardLimitAt"`
	FinishingAt    *time.Time `json:"finishingAt"`
	RescueDeadline *time.Time `json:"rescueDeadline"`
}
type RescueResult struct {
	Agent  string `json:"agent"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`
}

func (a API) call(ctx context.Context, method, path string, payload any, result any) error {
	if a.Store == nil || a.BaseURL == "" {
		return fmt.Errorf("run API not configured")
	}
	machine, err := a.Store.LoadMachine()
	if err != nil {
		return err
	}
	var body io.Reader
	if payload != nil {
		data, e := json.Marshal(payload)
		if e != nil {
			return e
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.BaseURL, "/")+"/agent/v1/machines/me/run"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+machine.APIToken)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("run API %s HTTP %d", path, response.StatusCode)
	}
	if result != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(result)
	}
	return nil
}
func (a API) Status(ctx context.Context) (RunStatus, error) {
	var state RunStatus
	err := a.call(ctx, http.MethodGet, "", nil, &state)
	return state, err
}
func (a API) UploadLogin(ctx context.Context, login json.RawMessage) error {
	return a.call(ctx, http.MethodPut, "/chatgpt-login", map[string]any{"login": login}, nil)
}
func (a API) Finished(ctx context.Context, rescue []RescueResult, uploaded bool) error {
	return a.call(ctx, http.MethodPost, "/finished", map[string]any{"rescue": rescue, "loginUploaded": uploaded}, nil)
}

type TokenSource struct {
	API      API
	Now      func() time.Time
	mu       sync.Mutex
	token    string
	obtained time.Time
	expires  time.Time
}

func (s *TokenSource) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
func (s *TokenSource) Get(ctx context.Context, force bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if !force && s.token != "" && now.Sub(s.obtained) < 50*time.Minute && now.Before(s.expires.Add(-5*time.Minute)) {
		return s.token, nil
	}
	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := s.API.call(ctx, http.MethodPost, "/github-token", map[string]any{}, &result); err != nil {
		return "", err
	}
	if result.Token == "" || !result.ExpiresAt.After(now.Add(time.Minute)) {
		return "", fmt.Errorf("run API returned invalid GitHub token")
	}
	if s.API.Store != nil {
		if err := writeGHHosts(s.API.Store.Home(), result.Token); err != nil {
			return "", err
		}
	}
	s.token = result.Token
	s.obtained = now
	s.expires = result.ExpiresAt
	return result.Token, nil
}
