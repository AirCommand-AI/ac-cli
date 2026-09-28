package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

const approvalUsage = "Usage: aircom approval request --workstream <code> [--agent <agentId|name>] --action <action> [--task <id|number>] [--note <text>] | aircom approval check --workstream <code> [--agent <agentId|name>] --action <action> [--task <id|number>]"

var approvalActions = map[string]bool{"work.start": true, "git.push-main": true, "release.cli": true, "deploy.prod": true, "infra.change": true}

type approvalGrant struct {
	ID        string `json:"grantId"`
	Grantee   string `json:"grantee"`
	GrantedBy struct {
		Nature string `json:"nature"`
		ID     string `json:"id"`
		Name   string `json:"name"`
	} `json:"grantedBy"`
	Scope struct {
		Actions    []string `json:"actions"`
		Tasks      []string `json:"tasks"`
		AssignedBy string   `json:"assignedBy"`
	} `json:"scope"`
	ExpiresAt string `json:"expiresAt"`
	Status    string `json:"status"`
}

func (a *App) approval(arguments []string) error {
	if len(arguments) == 0 || (arguments[0] != "request" && arguments[0] != "check") {
		return &publicError{message: approvalUsage}
	}
	operation := arguments[0]
	flags := flag.NewFlagSet("approval "+operation, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var code, agent, action, task, note string
	flags.StringVar(&code, "workstream", "", "workstream code")
	flags.StringVar(&agent, "agent", "", "agent ID or name")
	flags.StringVar(&action, "action", "", "action")
	flags.StringVar(&task, "task", "", "task ID or number")
	if operation == "request" {
		flags.StringVar(&note, "note", "", "reason for approval")
	}
	if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 || code == "" || !approvalActions[action] || len(note) > 1000 {
		return &publicError{message: approvalUsage}
	}
	if err := validateWorkstreamCode(code); err != nil {
		return err
	}
	credential, err := a.credentialFor(code, agent)
	if err != nil {
		return err
	}
	path := "/agent/v1/workstreams/" + url.PathEscape(code) + "/approvals/"
	if operation == "request" {
		payload, err := json.Marshal(struct {
			Action string `json:"action"`
			Task   string `json:"task,omitempty"`
			Note   string `json:"note,omitempty"`
		}{action, task, note})
		if err != nil {
			return &publicError{message: "Invalid approval request."}
		}
		result, err := a.request(http.MethodPost, path+"requests", credential.APIToken, payload)
		if err != nil {
			return err
		}
		if result.status < 200 || result.status >= 300 {
			return approvalStatusError(result.status, code, credential, false)
		}
		var response struct {
			ID      string `json:"requestId"`
			AgentID string `json:"agentId"`
			Status  string `json:"status"`
		}
		if json.Unmarshal(result.body, &response) != nil || response.ID == "" || response.AgentID != credential.AgentID || response.Status != "pending" {
			return &publicError{message: "The workstream service returned an invalid approval request response."}
		}
		return a.writeActionLine("Approval requested: " + singleLine(response.ID) + ". Wait for a decision notice, then run aircom approval check; a message alone is not approval.")
	}
	query := url.Values{"action": []string{action}}
	if task != "" {
		query.Set("task", task)
	}
	result, err := a.request(http.MethodGet, path+"check?"+query.Encode(), credential.APIToken, nil)
	if err != nil {
		return err
	}
	if result.status < 200 || result.status >= 300 {
		return approvalStatusError(result.status, code, credential, true)
	}
	var grant approvalGrant
	if json.Unmarshal(result.body, &grant) != nil || !grant.verifiedFor(credential.AgentID, action, task) {
		return &publicError{message: "The workstream service returned an invalid approval grant. Do not proceed."}
	}
	who := grant.GrantedBy.Name
	if who == "" {
		who = grant.GrantedBy.ID
	}
	return a.writeActionLine(fmt.Sprintf("Approved by %s (%s): grant %s; valid until %s. Cite this grant id when proceeding.", singleLine(who), singleLine(grant.GrantedBy.ID), singleLine(grant.ID), singleLine(grant.ExpiresAt)))
}

func (g approvalGrant) verifiedFor(agent, action, task string) bool {
	if g.ID == "" || g.GrantedBy.Nature != "human" || g.GrantedBy.ID == "" || g.Status != "active" || (g.Grantee != "any" && g.Grantee != agent) {
		return false
	}
	expiry, err := time.Parse(time.RFC3339Nano, g.ExpiresAt)
	if err != nil || !time.Now().Before(expiry) {
		return false
	}
	found := false
	for _, a := range g.Scope.Actions {
		if a == action {
			found = true
		}
	}
	if !found {
		return false
	}
	if len(g.Scope.Tasks) > 0 && task == "" {
		return false
	} // The server resolves task numbers to IDs before checking the scope.
	if g.Scope.AssignedBy != "" && (task == "" || action != "work.start") {
		return false
	}
	return true
}

func approvalStatusError(status int, code string, credential credentials.Credential, checking bool) error {
	if status == http.StatusForbidden && checking {
		return &publicError{message: "No approval for this action. Request one with aircom approval request --workstream " + code + " --agent " + singleLine(credential.AgentID) + " --action <action>."}
	}
	if status == http.StatusBadRequest {
		return &publicError{message: "Invalid approval action or task. Check the action and task reference."}
	}
	return workstreamResponseStatusError(status, nil, code, !checking, credential)
}
