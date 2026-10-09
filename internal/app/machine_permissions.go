package app

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const createWorkstreamUsage = "Usage: aircom workstream create --workspace <workspace> --name <name> [--description <text>] [--agent <agentId|name>]"
const permissionsUsage = "Usage: aircom permissions [--agent <agentId|name>]"

// Machine-level permission requests identify the registered agent separately
// from the machine bearer. The server verifies the agent belongs to this device.
func (a *App) machineAgentRequest(method, path, token, agent string, payload []byte) (httpResult, error) {
	request, err := http.NewRequest(method, strings.TrimRight(a.BaseURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return httpResult{}, &publicError{message: "The AirCommand service address is invalid."}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Agent-ID", agent)
	request.Header.Set("Accept", "application/json")
	if a.Organization != "" {
		request.Header.Set(organizationHeader, a.Organization)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := a.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	safe := *client
	if safe.CheckRedirect == nil {
		safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	response, err := safe.Do(request)
	if err != nil {
		return httpResult{}, &publicError{message: "Unable to connect to AirCommand."}
	}
	body, err := readResponse(response.Body)
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil {
		return httpResult{}, &publicError{message: "Unable to read the AirCommand response."}
	}
	return httpResult{status: response.StatusCode, body: body}, nil
}

func responseError(body []byte) string {
	var failure struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &failure) == nil {
		if failure.Error != "" {
			return singleLine(failure.Error)
		}
		if failure.Message != "" {
			return singleLine(failure.Message)
		}
	}
	return "request not permitted"
}

func (a *App) createWorkstream(args []string) error {
	if len(args) == 0 || args[0] != "create" {
		return &publicError{message: createWorkstreamUsage}
	}
	flags := flag.NewFlagSet("workstream create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workspace, name, description, agentRef string
	flags.StringVar(&workspace, "workspace", "", "workspace name or ID")
	flags.StringVar(&name, "name", "", "workstream name")
	flags.StringVar(&description, "description", "", "description")
	flags.StringVar(&agentRef, "agent", "", "agent name or ID")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || strings.TrimSpace(name) == "" || workspace == "" {
		return &publicError{message: createWorkstreamUsage}
	}
	org, err := a.resolveOrganization(workspace)
	if err != nil {
		return err
	}
	agent, err := a.resolveAgent(agentRef)
	if err != nil {
		return err
	}
	machine, err := a.machineCredential()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"workspace": org, "name": name, "description": description})
	previousOrg := a.Organization
	a.Organization = org
	defer func() { a.Organization = previousOrg }()
	response, err := a.machineAgentRequest(http.MethodPost, "/agent/v1/workstreams", machine.APIToken, agent.AgentID, payload)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return &publicError{message: fmt.Sprintf("Workstream creation rejected (HTTP %d): %s", response.status, responseError(response.body))}
	}
	var created struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(response.body, &created) != nil || created.Code == "" {
		return &publicError{message: "Invalid workstream creation response."}
	}
	if agent.WorkstreamCode != "" {
		return a.writeActionLine("Workstream " + created.Code + " created; " + agent.Name + " is still in workstream " + agent.WorkstreamCode)
	}
	// The server only creates; the normal join path obtains the agent session.
	if err := a.join([]string{"--agent", agent.AgentID, "--workspace", org, "--workstream", created.Code}); err != nil {
		return &publicError{message: "Workstream " + created.Code + " was created, but joining failed. Join it manually; do not create it again."}
	}
	return a.writeActionLine(agent.Name + " joined workstream " + created.Code)
}

func (a *App) permissions(args []string) error {
	flags := flag.NewFlagSet("permissions", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var agentRef string
	flags.StringVar(&agentRef, "agent", "", "agent name or ID")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return &publicError{message: permissionsUsage}
	}
	agent, err := a.resolveAgent(agentRef)
	if err != nil {
		return err
	}
	machine, err := a.machineCredential()
	if err != nil {
		return err
	}
	response, err := a.machineAgentRequest(http.MethodGet, "/agent/v1/machines/me/permissions", machine.APIToken, agent.AgentID, nil)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return &publicError{message: fmt.Sprintf("Permissions unavailable (HTTP %d): %s", response.status, responseError(response.body))}
	}
	var result struct {
		Permissions []struct {
			ID         string   `json:"id"`
			Kind       string   `json:"kind"`
			Workspaces []string `json:"workspaces"`
			Limits     any      `json:"limits"`
			ExpiresAt  *string  `json:"expiresAt"`
		} `json:"permissions"`
	}
	if json.Unmarshal(response.body, &result) != nil {
		return &publicError{message: "Invalid permissions response."}
	}
	if len(result.Permissions) == 0 {
		return a.writeActionLine("No active machine permissions.")
	}
	for _, permission := range result.Permissions {
		limits, _ := json.Marshal(permission.Limits)
		if permission.ID == "" || permission.Kind == "" {
			return &publicError{message: "Invalid permissions response."}
		}
		fmt.Fprintf(a.outputWriter(), "%s  %s  workspaces: %s  limits: %s\n", singleLine(permission.ID), singleLine(permission.Kind), strings.Join(permission.Workspaces, ","), limits)
	}
	return nil
}
