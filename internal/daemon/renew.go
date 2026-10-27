package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

const (
	// renewBefore is how close to expiry the daemon trades the machine token
	// for a fresh one. It matches the dashboard's renewal window.
	renewBefore = 7 * 24 * time.Hour
	// renewEvery is how often the daemon checks; a laptop that sleeps through
	// a check catches up at the next one.
	renewEvery = 6 * time.Hour
)

// TokenRenewer keeps this computer's machine token from running out, so the
// machine never has to be registered again while it is in use. Cloud run
// machines do not use it: their credential ends with their run.
type TokenRenewer struct {
	Store   *credentials.Store
	Client  *http.Client
	BaseURL string
	Now     func() time.Time
	Log     func(error)
}

// Run checks now and then every renewEvery until ctx ends.
func (r *TokenRenewer) Run(ctx context.Context) {
	for {
		if err := r.RenewIfDue(ctx); err != nil && r.Log != nil {
			r.Log(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(renewEvery):
		}
	}
}

type renewResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
}

// RenewIfDue renews the token when it expires within renewBefore, or when its
// expiry is unknown. A dashboard without renewal (404) is not an error.
func (r *TokenRenewer) RenewIfDue(ctx context.Context) error {
	machine, err := r.Store.LoadMachine()
	if err != nil {
		return nil
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	if expires, err := time.Parse(time.RFC3339, machine.ExpiresAt); err == nil && expires.After(now().Add(renewBefore)) {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.BaseURL, "/")+"/agent/v1/machines/me/token", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+machine.APIToken)
	req.Header.Set("Content-Type", "application/json")
	safeClient := *r.Client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := safeClient.Do(req)
	if err != nil {
		return fmt.Errorf("renew machine token: %w", err)
	}
	defer response.Body.Close()
	var result renewResponse
	switch {
	case response.StatusCode == http.StatusNotFound:
		return nil
	case response.StatusCode == http.StatusConflict:
		// Not due yet by the dashboard's clock; remember the expiry it reports.
		if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result) != nil || !validExpiry(result.ExpiresAt) || result.ExpiresAt == machine.ExpiresAt {
			return nil
		}
		machine.ExpiresAt = result.ExpiresAt
		return r.save(machine)
	case response.StatusCode < 200 || response.StatusCode > 299:
		return fmt.Errorf("renew machine token failed (HTTP %d); if it has expired, run aircom init", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result); err != nil || result.Token == "" || !validExpiry(result.ExpiresAt) {
		return fmt.Errorf("renew machine token: invalid response")
	}
	machine.APIToken, machine.ExpiresAt = result.Token, result.ExpiresAt
	return r.save(machine)
}

func (r *TokenRenewer) save(machine credentials.Machine) error {
	if err := r.Store.SaveMachine(machine); err != nil {
		return fmt.Errorf("save renewed machine token: %w", err)
	}
	return nil
}

func validExpiry(value string) bool {
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}
