package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/AirCommand-AI/ac-cli/internal/agentapi"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

// HTTPPoller uses the shared agent API wire contract. BaseURL is the same
// build-configured agent API URL as the CLI; no environment is read here.
type HTTPPoller struct {
	BaseURL string
	Client  *http.Client
	Store   *credentials.Store
	// Senders may fetch the workstream roster with the same agent credential.
	// An unavailable roster is harmless: summaries fall back to sender IDs.
	Senders func(context.Context, AgentDefinition) ([]agentapi.Sender, error)
}

func (p *HTTPPoller) Fetch(ctx context.Context, d AgentDefinition, cursor string, has bool) (Feed, error) {
	if p.Store == nil || p.Client == nil {
		return Feed{}, fmt.Errorf("notification API is not configured")
	}
	cred, err := p.Store.FindByAgent(d.Workstream, d.AgentID)
	if err != nil {
		return Feed{}, err
	}
	request := func(method, path, token string, payload []byte) (int, []byte, error) {
		base, err := url.Parse(p.BaseURL)
		if err != nil {
			return 0, nil, err
		}
		rel, err := url.Parse(path)
		if err != nil {
			return 0, nil, err
		}
		target := base.ResolveReference(rel)
		req, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := p.Client.Do(req)
		if err != nil {
			return 0, nil, fmt.Errorf("notification request failed: %w", err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
		if err != nil {
			return response.StatusCode, nil, err
		}
		if len(body) > 4*1024*1024 {
			return response.StatusCode, nil, fmt.Errorf("notification response too large")
		}
		return response.StatusCode, body, nil
	}
	feed, status, body, err := agentapi.Fetch(request, d.Workstream, cred.APIToken, cursor, has)
	var service struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &service)
	if agentapi.TerminalStatus(status, service.Code) {
		return Feed{}, ErrAgentStopped
	}
	if err != nil {
		return Feed{}, err
	}
	if status != http.StatusOK {
		return Feed{}, fmt.Errorf("%s", agentapi.FailureReason(status, service.Code))
	}
	return Feed{Notifications: feed.Notifications, Cursor: *feed.Cursor, PollAfter: agentapi.PollDelay(feed.PollAfterSeconds)}, nil
}
func (p *HTTPPoller) Spool(ctx context.Context, d AgentDefinition, n Notification) (any, error) {
	var names map[agentapi.SenderIdentity]string
	if p.Senders != nil {
		senders, err := p.Senders(ctx, d)
		if err == nil {
			names = agentapi.LoadSenderNames(senders)
		}
	}
	summary := agentapi.ComposeSummary(n, d.Workstream, names)
	return agentapi.Spool(n, summary), nil
}
