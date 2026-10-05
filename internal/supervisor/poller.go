package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentapi"
	"github.com/AirCommand-AI/ac-cli/internal/agentstate"
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

// ReportState sends the K1 snapshot through this agent's credential.
func (p *HTTPPoller) ReportState(ctx context.Context, d AgentDefinition, state agentstate.State, at time.Time) error {
	if p.Store == nil || p.Client == nil {
		return fmt.Errorf("state API is not configured")
	}
	cred, err := p.Store.FindByAgent(d.Workstream, d.AgentID)
	if err != nil {
		return err
	}
	return (agentstate.Reporter{BaseURL: p.BaseURL, Client: p.Client, Token: cred.APIToken}).Report(ctx, d.Workstream, state, at)
}

type APIStatusError struct{ Status int }

func (e *APIStatusError) Error() string { return fmt.Sprintf("agent API returned HTTP %d", e.Status) }

func (p *HTTPPoller) apiCall(ctx context.Context, d AgentDefinition, method, path string, payload any) ([]byte, error) {
	if p.Store == nil || p.Client == nil {
		return nil, fmt.Errorf("agent API is not configured")
	}
	cred, err := p.Store.FindByAgent(d.Workstream, d.AgentID)
	if err != nil {
		return nil, err
	}
	var body []byte
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cred.APIToken)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := p.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(result) > 4*1024*1024 {
		return nil, fmt.Errorf("agent API response too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &APIStatusError{Status: response.StatusCode}
	}
	return result, nil
}

type presenceTaskRow struct {
	ID, Status, Assignee string
	Number               int
}

// PresenceTasks provides K4's waiting precedence from agent-authenticated
// workstream data. Unlike the legacy stall probe, it checks pending approvals
// even when there is no in-flight task.
func (p *HTTPPoller) PresenceTasks(ctx context.Context, d AgentDefinition) (approval, blocked, inFlight string, err error) {
	base := "/agent/v1/workstreams/" + url.PathEscape(d.Workstream)
	body, err := p.apiCall(ctx, d, http.MethodGet, base, nil)
	if err != nil {
		return "", "", "", err
	}
	var detail struct {
		Tasks []presenceTaskRow `json:"tasks"`
	}
	if err = json.Unmarshal(body, &detail); err != nil {
		return "", "", "", err
	}
	label := func(t presenceTaskRow) string {
		if t.Number > 0 {
			return strconv.Itoa(t.Number)
		}
		return t.ID
	}
	for _, task := range detail.Tasks {
		if task.Assignee != d.AgentID {
			continue
		}
		switch task.Status {
		case "blocked":
			if blocked == "" {
				blocked = label(task)
			}
		case "in_flight":
			if inFlight == "" {
				inFlight = label(task)
			}
		}
	}
	approvalBody, err := p.apiCall(ctx, d, http.MethodGet, base+"/approvals/requests?mine=pending", nil)
	if err != nil {
		return "", "", "", err
	}
	var pending struct {
		Requests []struct {
			TaskID     string `json:"taskId"`
			TaskNumber int    `json:"taskNumber"`
		} `json:"requests"`
	}
	if err = json.Unmarshal(approvalBody, &pending); err != nil {
		return "", "", "", err
	}
	if len(pending.Requests) > 0 {
		approval = inFlight
		if pending.Requests[0].TaskNumber > 0 {
			approval = strconv.Itoa(pending.Requests[0].TaskNumber)
		}
		if approval == "" {
			approval = pending.Requests[0].TaskID
		}
	}
	return approval, blocked, inFlight, nil
}
func (p *HTTPPoller) MessageBody(ctx context.Context, d AgentDefinition, id string) (string, error) {
	body, err := p.apiCall(ctx, d, http.MethodGet, "/agent/v1/workstreams/"+url.PathEscape(d.Workstream)+"/messages/"+url.PathEscape(id), nil)
	if err != nil {
		return "", err
	}
	var message struct {
		Body string `json:"body"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(body, &message); err != nil {
		return "", err
	}
	if message.ID != id {
		return "", fmt.Errorf("message ID mismatch")
	}
	return message.Body, nil
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
