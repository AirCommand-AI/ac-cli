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
	Name                   string   `json:"name"`
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
		return fmt.Errorf("machine API %s returned HTTP %d", path, response.StatusCode)
	}
	if result == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(result)
}
func (a HTTPAPI) Seed(ctx context.Context, agents []Seed) error {
	return a.call(ctx, http.MethodPost, "/agent/v1/machines/me/agents/seed", map[string]any{"agents": agents}, nil)
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
