package supervisor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentapi"
	"github.com/AirCommand-AI/ac-cli/internal/agentstate"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/secrets"
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

// ReportState sends the K1 snapshot through this agent's credential. T5 will
// replace the legacy State/StateReason callers with the state engine.
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

// State reports headless pi event-derived state through the agent credential.
func (p *HTTPPoller) State(ctx context.Context, d AgentDefinition, state string) error {
	if p.Store == nil || p.Client == nil {
		return fmt.Errorf("state API is not configured")
	}
	cred, err := p.Store.FindByAgent(d.Workstream, d.AgentID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"state": state, "source": "daemon", "at": time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return err
	}
	base := strings.TrimRight(p.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/agent/v1/workstreams/"+url.PathEscape(d.Workstream)+"/agents/me/state", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cred.APIToken)
	req.Header.Set("Content-Type", "application/json")
	response, err := p.Client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("state API returned HTTP %d", response.StatusCode)
	}
	return nil
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

type stallTaskRow struct {
	ID, Status, Assignee, Milestone, CreatedAt string
	Number, Position                           int
}
type stallMilestoneRow struct {
	Name     string
	Position int
}

// Mirrors overview.go's milestone groups followed by position (0 last),
// task number, createdAt and ID. Only assigned in-flight tasks are candidates;
// all tasks still determine group order.
func chooseInFlightTask(rows []stallTaskRow, milestones []stallMilestoneRow, agentID string) (InFlightTask, bool) {
	groups := map[string]int{}
	for _, row := range rows {
		key := strings.TrimSpace(row.Milestone)
		if n, ok := groups[key]; !ok || row.Number < n {
			groups[key] = row.Number
		}
	}
	ordered := make(map[string]int)
	for _, m := range milestones {
		ordered[m.Name] = m.Position
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a == "" || b == "" {
			return a != ""
		}
		ap, aok := ordered[a]
		bp, bok := ordered[b]
		if aok != bok {
			return aok
		}
		if aok && ap != bp {
			return ap < bp
		}
		if groups[a] != groups[b] {
			return groups[a] < groups[b]
		}
		return a < b
	})
	rank := make(map[string]int, len(keys))
	for i, k := range keys {
		rank[k] = i
	}
	candidates := make([]stallTaskRow, 0)
	for _, row := range rows {
		if row.Assignee == agentID && row.Status == "in_flight" {
			candidates = append(candidates, row)
		}
	}
	if len(candidates) == 0 {
		return InFlightTask{}, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		ar, br := rank[strings.TrimSpace(a.Milestone)], rank[strings.TrimSpace(b.Milestone)]
		if ar != br {
			return ar < br
		}
		if a.Position == 0 && b.Position != 0 {
			return false
		}
		if b.Position == 0 && a.Position != 0 {
			return true
		}
		if a.Position != b.Position {
			return a.Position < b.Position
		}
		if a.Number != b.Number {
			return a.Number < b.Number
		}
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt < b.CreatedAt
		}
		return a.ID < b.ID
	})
	first := candidates[0]
	return InFlightTask{ID: first.ID, Number: first.Number, Position: first.Position}, true
}

func (p *HTTPPoller) InFlight(ctx context.Context, d AgentDefinition) (InFlightTask, bool, bool, error) {
	base := "/agent/v1/workstreams/" + url.PathEscape(d.Workstream)
	body, err := p.apiCall(ctx, d, http.MethodGet, base, nil)
	if err != nil {
		return InFlightTask{}, false, false, err
	}
	var detail struct {
		Tasks []stallTaskRow `json:"tasks"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		return InFlightTask{}, false, false, err
	}
	selected, found := chooseInFlightTask(detail.Tasks, nil, d.AgentID)
	if !found {
		return InFlightTask{}, false, false, nil
	}
	// Fetch milestone order only when candidate tasks span groups.
	groups := map[string]struct{}{}
	for _, task := range detail.Tasks {
		if task.Assignee == d.AgentID && task.Status == "in_flight" {
			groups[strings.TrimSpace(task.Milestone)] = struct{}{}
		}
	}
	if len(groups) > 1 {
		milestoneBody, err := p.apiCall(ctx, d, http.MethodGet, base+"/milestones", nil)
		if err != nil {
			return InFlightTask{}, false, false, err
		}
		var milestones []stallMilestoneRow
		if err := json.Unmarshal(milestoneBody, &milestones); err != nil {
			return InFlightTask{}, false, false, err
		}
		selected, _ = chooseInFlightTask(detail.Tasks, milestones, d.AgentID)
	}
	approval, err := p.apiCall(ctx, d, http.MethodGet, base+"/approvals/requests?mine=pending", nil)
	if err != nil {
		return InFlightTask{}, false, false, err
	}
	var pending struct {
		Requests []json.RawMessage `json:"requests"`
	}
	if err := json.Unmarshal(approval, &pending); err != nil {
		return InFlightTask{}, false, false, err
	}
	return selected, found, len(pending.Requests) > 0, nil
}
func (p *HTTPPoller) StateReason(ctx context.Context, d AgentDefinition, state, reason string) error {
	payload := map[string]string{"state": state, "reason": reason, "source": "daemon", "at": time.Now().UTC().Format(time.RFC3339Nano)}
	_, err := p.apiCall(ctx, d, http.MethodPut, "/agent/v1/workstreams/"+url.PathEscape(d.Workstream)+"/agents/me/state", payload)
	return err
}
func (p *HTTPPoller) NudgeUpdate(ctx context.Context, d AgentDefinition, task InFlightTask) error {
	id, err := secrets.IdempotencyID(rand.Reader)
	if err != nil {
		return err
	}
	payload := map[string]string{"summary": fmt.Sprintf("Auto-nudged after 15m without progress on %s", taskLabel(task)), "taskId": task.ID, "idempotencyId": id}
	_, err = p.apiCall(ctx, d, http.MethodPost, "/agent/v1/workstreams/"+url.PathEscape(d.Workstream)+"/updates", payload)
	return err
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
