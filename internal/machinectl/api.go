package machinectl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

type Agent struct {
	AgentID                string   `json:"agentId"`
	Name                   string   `json:"name"`
	Desired                string   `json:"desired"`
	Mode                   string   `json:"mode"`
	Repos                  []string `json:"repos"`
	WorkFolder             string   `json:"workFolder"`
	AssignedOrganizationID string   `json:"assignedOrganizationId"`
	AssignedWorkstreamCode string   `json:"assignedWorkstreamCode"`
	JoinedOrganizationID   string   `json:"joinedOrganizationId"`
	JoinedWorkstreamCode   string   `json:"joinedWorkstreamCode"`
	Revision               int64    `json:"revision"`
}

type Definitions struct {
	Machine struct {
		State   string `json:"state"`
		StateAt string `json:"stateAt"`
	} `json:"machine"`
	Agents []Agent `json:"agents"`
}

type Seed struct {
	AgentID                string   `json:"agentId"`
	Desired                string   `json:"desired"`
	Mode                   string   `json:"mode"`
	Repos                  []string `json:"repos"`
	WorkFolder             string   `json:"workFolder"`
	AssignedOrganizationID string   `json:"assignedOrganizationId"`
	AssignedWorkstreamCode string   `json:"assignedWorkstreamCode"`
}

type API interface {
	Seed(context.Context, []Seed) error
	Definitions(context.Context) (Definitions, error)
	Result(context.Context, string, int64, string, string) error
	Desired(context.Context, string, string, string) (int64, error)
}

// HTTPAPI authenticates every machine route with the device bearer. No machine
// control instruction is accepted from a WebSocket frame.
// HTTPStatusError lets the reconciler distinguish permanent roster conflicts
// from temporary transport failures when retrying local desired updates.
type HTTPStatusError struct {
	Path   string
	Status int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("machine API %s returned HTTP %d", e.Path, e.Status)
}
func (e *HTTPStatusError) HTTPStatus() int { return e.Status }

type HTTPAPI struct {
	BaseURL string
	Client  *http.Client
	Store   *credentials.Store
}

func (a HTTPAPI) call(ctx context.Context, method, path string, payload any, result any) error {
	if a.Client == nil || a.Store == nil {
		return fmt.Errorf("machine API is not configured")
	}
	machine, err := a.Store.LoadMachine()
	if err != nil {
		return err
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+machine.APIToken)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := a.Client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &HTTPStatusError{Path: path, Status: response.StatusCode}
	}
	if result == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(result)
}

// SeedResultError identifies a result for one agent that the server did not
// seed. A skipped row is permanently missing/inactive for this daemon run;
// a missing or malformed result is retried rather than silently accepted.
type SeedResultError struct {
	AgentID string
	Status  string
	Reason  string
}

func (e *SeedResultError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("seed %s: %s: %s", e.AgentID, e.Status, e.Reason)
	}
	return fmt.Sprintf("seed %s: %s", e.AgentID, e.Status)
}
func (e *SeedResultError) Permanent() bool { return e.Status == "skipped" }

type SeedResultsError struct{ Failures []*SeedResultError }

func (e *SeedResultsError) Error() string {
	parts := make([]string, 0, len(e.Failures))
	for _, failure := range e.Failures {
		parts = append(parts, failure.Error())
	}
	return strings.Join(parts, "; ")
}

func (a HTTPAPI) Seed(ctx context.Context, agents []Seed) error {
	var response struct {
		Results []struct {
			AgentID string `json:"agentId"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
		} `json:"results"`
	}
	if err := a.call(ctx, http.MethodPost, "/agent/v1/machines/me/agents/seed", map[string]any{"agents": agents}, &response); err != nil {
		return err
	}
	requested := make(map[string]bool, len(agents))
	for _, agent := range agents {
		if agent.AgentID == "" || requested[agent.AgentID] {
			return fmt.Errorf("duplicate or empty seed agent ID %q", agent.AgentID)
		}
		requested[agent.AgentID] = true
	}
	seen := make(map[string]bool, len(response.Results))
	failures := &SeedResultsError{}
	for _, result := range response.Results {
		if !requested[result.AgentID] || seen[result.AgentID] {
			return fmt.Errorf("seed response has unexpected or duplicate agent ID %q", result.AgentID)
		}
		seen[result.AgentID] = true
		if result.Status != "seeded" {
			failures.Failures = append(failures.Failures, &SeedResultError{AgentID: result.AgentID, Status: result.Status, Reason: result.Reason})
		}
	}
	for id := range requested {
		if !seen[id] {
			failures.Failures = append(failures.Failures, &SeedResultError{AgentID: id, Status: "missing result"})
		}
	}
	if len(failures.Failures) == 0 {
		return nil
	}
	if len(failures.Failures) == 1 {
		return failures.Failures[0]
	}
	return failures
}
func (a HTTPAPI) Definitions(ctx context.Context) (Definitions, error) {
	var result Definitions
	err := a.call(ctx, http.MethodGet, "/agent/v1/machines/me/agents", nil, &result)
	return result, err
}
func (a HTTPAPI) Result(ctx context.Context, id string, revision int64, status, reason string) error {
	return a.call(ctx, http.MethodPost, "/agent/v1/machines/me/agents/"+url.PathEscape(id)+"/result", map[string]any{"revision": revision, "status": status, "reason": reason}, nil)
}
func (a HTTPAPI) Desired(ctx context.Context, id, desired, mode string) (int64, error) {
	var result struct {
		Revision int64 `json:"revision"`
	}
	err := a.call(ctx, http.MethodPost, "/agent/v1/machines/me/agents/"+url.PathEscape(id)+"/desired", map[string]string{"desired": desired, "mode": mode}, &result)
	return result.Revision, err
}
