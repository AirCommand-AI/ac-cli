package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

const milestonesUsage = "Usage: aircom milestones --workstream <code> [--agent <agentId|name>]"
const milestoneUsage = "Usage: aircom milestone <name> --workstream <code> [--agent <agentId|name>] [--position <n> | --before <name> | --after <name>] [--target <YYYY-MM-DD>] [--description <text>] [--rename <name>]"

type milestoneItem struct {
	Name        string `json:"name"`
	Position    int    `json:"position"`
	TargetDate  string `json:"targetDate"`
	Description string `json:"description"`
	Count       int    `json:"count"`
}
type movePositionRequest struct {
	Position    *int    `json:"position,omitempty"`
	Before      string  `json:"before,omitempty"`
	After       string  `json:"after,omitempty"`
	Target      *string `json:"target,omitempty"`
	Description *string `json:"description,omitempty"`
	Rename      *string `json:"rename,omitempty"`
}

func (a *App) readMilestones(code string, credential credentials.Credential) ([]milestoneItem, error) {
	response, err := a.request(http.MethodGet, "/agent/v1/workstreams/"+code+"/milestones", credential.APIToken, nil)
	if err != nil {
		return nil, err
	}
	if response.status < 200 || response.status >= 300 {
		return nil, workstreamResponseStatusError(response.status, response.body, code, false, credential)
	}
	var rows []milestoneItem
	if json.Unmarshal(response.body, &rows) != nil {
		return nil, &publicError{message: "The workstream service returned an invalid milestone response."}
	}
	return rows, nil
}
func (a *App) milestones(args []string) error {
	flags := flag.NewFlagSet("milestones", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var code, agent string
	flags.StringVar(&code, "workstream", "", "workstream code")
	flags.StringVar(&agent, "agent", "", "agent ID")
	if flags.Parse(args) != nil || flags.NArg() != 0 || code == "" {
		return &publicError{message: milestonesUsage}
	}
	if err := validateWorkstreamCode(code); err != nil {
		return err
	}
	credential, err := a.credentialFor(code, agent)
	if err != nil {
		return err
	}
	rows, err := a.readMilestones(code, credential)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		_, err = fmt.Fprintln(a.outputWriter(), "No milestones yet.")
		return err
	}
	for _, m := range rows {
		if _, err := fmt.Fprintf(a.outputWriter(), "%d\t%s\t%d tasks\t%s\n", m.Position, safeMetadata(m.Name, credential.APIToken, credential.SocketKey), m.Count, m.TargetDate); err != nil {
			return &publicError{message: "Unable to write milestone output."}
		}
	}
	return nil
}
func (a *App) milestone(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return &publicError{message: milestoneUsage}
	}
	name := strings.TrimSpace(args[0])
	flags := flag.NewFlagSet("milestone", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var code, agent, before, after, target, description, rename string
	var position int
	flags.StringVar(&code, "workstream", "", "workstream code")
	flags.StringVar(&agent, "agent", "", "agent ID")
	flags.IntVar(&position, "position", 0, "milestone position")
	flags.StringVar(&before, "before", "", "place before milestone")
	flags.StringVar(&after, "after", "", "place after milestone")
	flags.StringVar(&target, "target", "", "target date")
	flags.StringVar(&description, "description", "", "description")
	flags.StringVar(&rename, "rename", "", "new name")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || code == "" || name == "" {
		return &publicError{message: milestoneUsage}
	}
	if err := validateWorkstreamCode(code); err != nil {
		return err
	}
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["position"] && position <= 0 || set["position"] && (set["before"] || set["after"]) || set["before"] && set["after"] || set["rename"] && strings.TrimSpace(rename) == "" {
		return &publicError{message: milestoneUsage}
	}
	credential, err := a.credentialFor(code, agent)
	if err != nil {
		return err
	}
	req := movePositionRequest{Before: before, After: after}
	if set["position"] {
		req.Position = &position
	}
	if set["target"] {
		req.Target = &target
	}
	if set["description"] {
		req.Description = &description
	}
	if set["rename"] {
		req.Rename = &rename
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return &publicError{message: "Invalid milestone request."}
	}
	response, err := a.request(http.MethodPatch, "/agent/v1/workstreams/"+code+"/milestones/"+url.PathEscape(name), credential.APIToken, payload)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return workstreamResponseStatusError(response.status, response.body, code, false, credential)
	}
	_, err = fmt.Fprintf(a.outputWriter(), "Milestone %s saved.\n", safeMetadata(name, credential.APIToken, credential.SocketKey))
	return err
}
func (a *App) moveTask(code, id string, position *int, before, after string, credential credentials.Credential) error {
	req := movePositionRequest{Position: position, Before: before, After: after}
	payload, err := json.Marshal(req)
	if err != nil {
		return &publicError{message: "Invalid task position request."}
	}
	response, err := a.request(http.MethodPatch, "/agent/v1/workstreams/"+code+"/tasks/"+url.PathEscape(id)+"/position", credential.APIToken, payload)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return workstreamResponseStatusError(response.status, response.body, code, false, credential)
	}
	_, err = fmt.Fprintf(a.outputWriter(), "Task %s position saved.\n", safeMetadata(id, credential.APIToken, credential.SocketKey))
	return err
}
