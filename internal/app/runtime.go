package app

import (
	"encoding/json"
	"flag"
	"io"
	"net/http"
)

// runtime is internal adapter transport, not an agent-facing instruction.
func (a *App) runtime(arguments []string) error {
	const usage = "Usage: aircom runtime --workstream <code> --agent <id> --json <payload>"
	flags := flag.NewFlagSet("runtime", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var code, agent, payload string
	flags.StringVar(&code, "workstream", "", "workstream code")
	flags.StringVar(&agent, "agent", "", "agent ID")
	flags.StringVar(&payload, "json", "", "runtime JSON payload")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || code == "" || agent == "" || len(payload) > 8192 {
		return &publicError{message: usage}
	}
	if err := validateWorkstreamCode(code); err != nil {
		return err
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(payload), &body); err != nil || body == nil {
		return &publicError{message: usage}
	}
	credential, err := a.credentialFor(code, agent)
	if err != nil {
		return err
	}
	response, err := a.request(http.MethodPut, "/agent/v1/workstreams/"+code+"/agents/me/runtime", credential.APIToken, []byte(payload))
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return workstreamResponseStatusError(response.status, response.body, code, true, credential)
	}
	return nil
}
