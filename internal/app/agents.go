package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// This file owns the three-level registration the product describes: a machine
// belongs to an account (aircom init), an agent belongs to a machine (connect),
// and an agent joins one workstream at a time (join / leave).

type registerAgentRequest struct {
	Name string `json:"name"`
}

type agentSummary struct {
	AgentID        string `json:"agentId"`
	Name           string `json:"name"`
	Status         string `json:"status"`
	DeviceID       string `json:"deviceId"`
	OrganizationID string `json:"organizationId"`
	WorkstreamCode string `json:"workstreamCode"`
}

type listAgentsResponse struct {
	Agents []agentSummary `json:"agents"`
}

type organizationSummary struct {
	OrganizationID string `json:"organizationId"`
	Name           string `json:"name"`
}

type listOrganizationsResponse struct {
	Organizations []organizationSummary `json:"organizations"`
}

const (
	connectUsage    = "Usage: aircom connect --name <agentName>"
	agentsUsage     = "Usage: aircom agents"
	orgsUsage       = "Usage: aircom orgs"
	leaveUsage      = "Usage: aircom leave --agent <agentId|name>"
	disconnectUsage = "Usage: aircom disconnect --agent <agentId|name>"
)

// connect registers this runtime as an agent on this machine.
//
// It joins nothing: the agent exists, on this laptop, in no workstream. Joining
// is a separate act, so an agent can move between workstreams while staying the
// same agent.
func (a *App) connect(arguments []string) error {
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var name string
	flags.StringVar(&name, "name", "", "name this agent answers to on this machine")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return &publicError{message: connectUsage}
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return &publicError{message: connectUsage}
	}
	machine, err := a.machineCredential()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(registerAgentRequest{Name: name})
	if err != nil {
		return &publicError{message: "Unable to prepare the connect request."}
	}
	response, err := a.request(http.MethodPost, "/v1/agents", machine.APIToken, payload)
	if err != nil {
		return err
	}
	switch {
	case response.status == http.StatusConflict:
		return &publicError{message: fmt.Sprintf("An agent on this machine is already called %q. Choose another name.", name)}
	case response.status == http.StatusUnauthorized, response.status == http.StatusForbidden:
		return &publicError{message: "This machine's registration is no longer valid. Run aircom init again."}
	case response.status < 200 || response.status >= 300:
		return &publicError{message: "Unable to connect this agent."}
	}
	var agent agentSummary
	if err := json.Unmarshal(response.body, &agent); err != nil || agent.AgentID == "" {
		return &publicError{message: "The AirCommand service returned an invalid connect response."}
	}
	fmt.Fprintf(a.outputWriter(), "Connected as %s (%s).\n\nThis agent is in no workstream yet. Join one with:\n    aircom join --agent %s --org <org> --workstream <code>\n",
		agent.Name, agent.AgentID, agent.Name)
	return nil
}

// agents lists what is running on this machine.
func (a *App) agents(arguments []string) error {
	if len(arguments) != 0 {
		return &publicError{message: agentsUsage}
	}
	list, err := a.fetchAgents()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(a.outputWriter(), "No agents on this machine yet. Register one with: aircom connect --name <agentName>")
		return nil
	}
	for _, agent := range list {
		where := "not in a workstream"
		if strings.TrimSpace(agent.WorkstreamCode) != "" {
			where = "workstream " + agent.WorkstreamCode
		}
		fmt.Fprintf(a.outputWriter(), "%-20s %-40s %s\n", agent.Name, agent.AgentID, where)
	}
	return nil
}

// orgs lists the organizations this machine can reach, so --org can be written
// as a name rather than an opaque identifier.
func (a *App) orgs(arguments []string) error {
	if len(arguments) != 0 {
		return &publicError{message: orgsUsage}
	}
	list, err := a.fetchOrganizations()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(a.outputWriter(), "This machine can reach no organizations.")
		return nil
	}
	for _, organization := range list {
		fmt.Fprintf(a.outputWriter(), "%-30s %s\n", organization.Name, organization.OrganizationID)
	}
	return nil
}

func (a *App) fetchAgents() ([]agentSummary, error) {
	machine, err := a.machineCredential()
	if err != nil {
		return nil, err
	}
	response, err := a.request(http.MethodGet, "/v1/agents", machine.APIToken, nil)
	if err != nil {
		return nil, err
	}
	if response.status == http.StatusUnauthorized || response.status == http.StatusForbidden {
		return nil, &publicError{message: "This machine's registration is no longer valid. Run aircom init again."}
	}
	if response.status < 200 || response.status >= 300 {
		return nil, &publicError{message: "Unable to list this machine's agents."}
	}
	var decoded listAgentsResponse
	if err := json.Unmarshal(response.body, &decoded); err != nil {
		return nil, &publicError{message: "The AirCommand service returned an invalid agent list."}
	}
	return decoded.Agents, nil
}

func (a *App) fetchOrganizations() ([]organizationSummary, error) {
	machine, err := a.machineCredential()
	if err != nil {
		return nil, err
	}
	response, err := a.request(http.MethodGet, "/v1/organizations", machine.APIToken, nil)
	if err != nil {
		return nil, err
	}
	if response.status == http.StatusUnauthorized || response.status == http.StatusForbidden {
		return nil, &publicError{message: "This machine's registration is no longer valid. Run aircom init again."}
	}
	if response.status < 200 || response.status >= 300 {
		return nil, &publicError{message: "Unable to list organizations."}
	}
	var decoded listOrganizationsResponse
	if err := json.Unmarshal(response.body, &decoded); err != nil {
		return nil, &publicError{message: "The AirCommand service returned an invalid organization list."}
	}
	return decoded.Organizations, nil
}

// resolveOrganization turns --org into an identifier. It accepts the identifier
// itself or a name, because the dashboard shows names and logs show
// identifiers, and a human should be able to use whichever is in front of them.
//
// Ties fail closed rather than picking one: two organizations sharing a name is
// exactly when guessing does the most damage.
func (a *App) resolveOrganization(reference string) (string, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return "", &publicError{message: "Name the organization with --org. Run aircom orgs to see them."}
	}
	list, err := a.fetchOrganizations()
	if err != nil {
		return "", err
	}
	for _, organization := range list {
		if organization.OrganizationID == reference {
			return organization.OrganizationID, nil
		}
	}

	var exact, folded []organizationSummary
	for _, organization := range list {
		switch {
		case organization.Name == reference:
			exact = append(exact, organization)
		case strings.EqualFold(strings.TrimSpace(organization.Name), reference):
			folded = append(folded, organization)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = folded
	}
	switch len(matches) {
	case 1:
		return matches[0].OrganizationID, nil
	case 0:
		names := make([]string, 0, len(list))
		for _, organization := range list {
			names = append(names, organization.Name)
		}
		sort.Strings(names)
		if len(names) == 0 {
			return "", &publicError{message: "This machine can reach no organizations."}
		}
		return "", &publicError{message: fmt.Sprintf("No organization called %q. This machine can reach: %s", reference, strings.Join(names, ", "))}
	default:
		ids := make([]string, 0, len(matches))
		for _, organization := range matches {
			ids = append(ids, organization.OrganizationID)
		}
		sort.Strings(ids)
		return "", &publicError{message: fmt.Sprintf("More than one organization is called %q. Use its identifier: %s", reference, strings.Join(ids, ", "))}
	}
}

// resolveAgent turns --agent into an agent on this machine, by id or by name.
func (a *App) resolveAgent(reference string) (agentSummary, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return agentSummary{}, &publicError{message: "Name the agent with --agent. Run aircom agents to see them."}
	}
	list, err := a.fetchAgents()
	if err != nil {
		return agentSummary{}, err
	}
	for _, agent := range list {
		if agent.AgentID == reference {
			return agent, nil
		}
	}
	var exact, folded []agentSummary
	for _, agent := range list {
		switch {
		case agent.Name == reference:
			exact = append(exact, agent)
		case strings.EqualFold(strings.TrimSpace(agent.Name), reference):
			folded = append(folded, agent)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = folded
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return agentSummary{}, &publicError{message: fmt.Sprintf("No agent on this machine called %q. Run aircom agents to see them.", reference)}
	default:
		ids := make([]string, 0, len(matches))
		for _, agent := range matches {
			ids = append(ids, agent.AgentID)
		}
		sort.Strings(ids)
		return agentSummary{}, &publicError{message: fmt.Sprintf("More than one agent is called %q. Use its id: %s", reference, strings.Join(ids, ", "))}
	}
}

// leave takes an agent out of its workstream, freeing it to join another.
func (a *App) leave(arguments []string) error {
	flags := flag.NewFlagSet("leave", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var agentReference string
	flags.StringVar(&agentReference, "agent", "", "agent id or name")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return &publicError{message: leaveUsage}
	}
	machine, err := a.machineCredential()
	if err != nil {
		return err
	}
	agent, err := a.resolveAgent(agentReference)
	if err != nil {
		return err
	}
	response, err := a.request(http.MethodDelete, "/v1/agents/"+agent.AgentID+"/workstream", machine.APIToken, nil)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return &publicError{message: "Unable to take that agent out of its workstream."}
	}
	fmt.Fprintf(a.outputWriter(), "%s is no longer in a workstream.\n", agent.Name)
	return nil
}

// disconnect removes an agent from this machine for good. If it is in a
// workstream it leaves first, so no roster entry or credential is left behind,
// and its name becomes free to reuse.
func (a *App) disconnect(arguments []string) error {
	flags := flag.NewFlagSet("disconnect", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var agentReference string
	flags.StringVar(&agentReference, "agent", "", "agent id or name")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return &publicError{message: disconnectUsage}
	}
	machine, err := a.machineCredential()
	if err != nil {
		return err
	}
	agent, err := a.resolveAgent(agentReference)
	if err != nil {
		return err
	}
	response, err := a.request(http.MethodDelete, "/v1/agents/"+agent.AgentID, machine.APIToken, nil)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return &publicError{message: "Unable to remove that agent."}
	}
	fmt.Fprintf(a.outputWriter(), "%s has been removed from this machine.\n", agent.Name)
	return nil
}
