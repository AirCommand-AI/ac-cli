package app

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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
	type permissionView struct {
		ID             string   `json:"id"`
		Kind           string   `json:"kind"`
		Workspaces     []string `json:"workspaces"`
		WorkspaceNames []string `json:"workspaceNames"`
		ProfileNames   []string `json:"profileNames"`
		DefaultGrants  []struct {
			Action string `json:"action"`
		} `json:"defaultGrants"`
		Limits *struct {
			ProfileIDs    []string `json:"profileIds"`
			MaxAgents     int      `json:"maxAgents"`
			MaxRuntimeMin int      `json:"maxRuntimeMin"`
			MaxActive     int      `json:"maxActive"`
		} `json:"limits"`
		ExpiresAt *string `json:"expiresAt"`
	}
	var result struct {
		Permissions []permissionView `json:"permissions"`
	}
	if json.Unmarshal(response.body, &result) != nil {
		return &publicError{message: "Invalid permissions response."}
	}
	if len(result.Permissions) == 0 {
		return a.writeActionLine("No active machine permissions.")
	}
	for _, permission := range result.Permissions {
		if permission.ID == "" {
			return &publicError{message: "Invalid permissions response."}
		}
		expiry := "no expiry"
		if permission.ExpiresAt != nil && *permission.ExpiresAt != "" {
			parsed, parseErr := time.Parse(time.RFC3339, *permission.ExpiresAt)
			if parseErr == nil {
				expiry = "expires " + parsed.UTC().Format("2006-01-02")
			} else {
				expiry = "expires " + singleLine(*permission.ExpiresAt)
			}
		}
		switch permission.Kind {
		case "workstream.create":
			workspaces := permission.WorkspaceNames
			if len(workspaces) != len(permission.Workspaces) {
				workspaces = permission.Workspaces
			}
			grants := make([]string, 0, len(permission.DefaultGrants))
			for _, grant := range permission.DefaultGrants {
				switch grant.Action {
				case "work.start":
					grants = append(grants, "Start work on tasks")
				case "git.push-main":
					grants = append(grants, "Push to main")
				case "machine.run":
					grants = append(grants, "Start cloud machines")
				default:
					grants = append(grants, singleLine(grant.Action))
				}
			}
			if len(grants) == 0 {
				grants = append(grants, "none")
			}
			fmt.Fprintf(a.outputWriter(), "Create workstreams in: %s (default grants: %s); %s\n", strings.Join(workspaces, ", "), strings.Join(grants, ", "), expiry)
		case "machine.run":
			if permission.Limits == nil {
				return &publicError{message: "Invalid machine permission limits."}
			}
			profiles := permission.ProfileNames
			if len(profiles) != len(permission.Limits.ProfileIDs) {
				profiles = permission.Limits.ProfileIDs
			}
			minutes := permission.Limits.MaxRuntimeMin
			duration := fmt.Sprintf("%d min", minutes)
			if minutes%60 == 0 {
				duration = fmt.Sprintf("%d h", minutes/60)
			}
			if minutes > 60 && minutes%60 != 0 {
				duration = fmt.Sprintf("%d h %d min", minutes/60, minutes%60)
			}
			fmt.Fprintf(a.outputWriter(), "Start cloud machines: %s; up to %d agents, %s, %d at once; %s\n", strings.Join(profiles, ", "), permission.Limits.MaxAgents, duration, permission.Limits.MaxActive, expiry)
		default:
			return &publicError{message: "Unknown machine permission kind."}
		}
	}
	return nil
}
