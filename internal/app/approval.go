package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

const approvalUsage = "Usage: aircom approval request|check --workstream <code> [--agent <agentId|name>] --action <action> [--task <id|number>] [--max-agents N] [--max-runtime-min N] [--profile <name>] [--repo <owner/repo>]... [--allow-from-run-machine] [--note <text> (request only)]"

var approvalActions = map[string]bool{"work.start": true, "git.push-main": true, "release.cli": true, "deploy.prod": true, "infra.change": true, "machine.run": true, "machine.done": true}

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
	var code, agent, action, task, note, profile string
	var maxAgents, maxRuntime int
	var allowFromRun bool
	var repos stringList
	flags.StringVar(&code, "workstream", "", "workstream code")
	flags.StringVar(&agent, "agent", "", "agent ID or name")
	flags.StringVar(&action, "action", "", "action")
	flags.StringVar(&task, "task", "", "task ID or number")
	flags.IntVar(&maxAgents, "max-agents", 0, "maximum agents for machine.run")
	flags.IntVar(&maxRuntime, "max-runtime-min", 0, "maximum runtime minutes for machine.run")
	flags.StringVar(&profile, "profile", "", "machine.run profile")
	flags.Var(&repos, "repo", "machine.run repository (repeatable)")
	flags.BoolVar(&allowFromRun, "allow-from-run-machine", false, "allow machine.run from an agent on a run machine")
	if operation == "request" {
		flags.StringVar(&note, "note", "", "reason for approval")
	}
	if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 || code == "" || action == "" || len(note) > 1000 || maxAgents < 0 || maxRuntime < 0 || (action != "machine.run" && (maxAgents != 0 || maxRuntime != 0 || profile != "" || len(repos.values) > 0 || allowFromRun)) {
		return &publicError{message: approvalUsage}
	}
	if !approvalActions[action] {
		return &publicError{message: unknownApprovalAction(action)}
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
			Scope  any    `json:"scope,omitempty"`
		}{action, task, note, approvalMachineScope(action, maxAgents, maxRuntime, profile, repos.values, allowFromRun)})
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
	if action == "machine.run" {
		if maxAgents > 0 {
			query.Set("maxAgents", fmt.Sprint(maxAgents))
		}
		if maxRuntime > 0 {
			query.Set("maxRuntimeMin", fmt.Sprint(maxRuntime))
		}
		if profile != "" {
			query.Set("profile", profile)
		}
		for _, repo := range repos.values {
			query.Add("repo", repo)
		}
		if allowFromRun {
			query.Set("allowFromRunMachine", "true")
		}
	}
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

// approvalActionOrder lists approvalActions for error messages, most common first.
var approvalActionOrder = []string{"work.start", "git.push-main", "release.cli", "deploy.prod", "infra.change", "machine.run", "machine.done"}

// unknownApprovalAction names the valid actions so an agent that guessed one
// (e.g. git.push, ac-cli#18 follow-up) learns there is nothing else to ask for.
func approvalMachineScope(action string, maxAgents, maxRuntime int, profile string, repos []string, allow bool) any {
	if action != "machine.run" {
		return nil
	}
	return map[string]any{"actions": []string{action}, "maxAgents": maxAgents, "maxRuntimeMin": maxRuntime, "profile": profile, "repos": repos, "allowFromRunMachine": allow}
}

func unknownApprovalAction(action string) string {
	return fmt.Sprintf("Unknown approval action %q. The only actions are %s. "+
		"A passing work.start check already covers a task's normal work, including committing and pushing feature branches.",
		action, strings.Join(approvalActionOrder, ", "))
}
