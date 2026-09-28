package app

import (
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"time"
)

// state is adapter plumbing; it is intentionally absent from agent guidance.
func (a *App) state(arguments []string) error {
	const usage = "Usage: aircom state --workstream <code> --agent <id> <working|idle> --source <name>"
	flags := flag.NewFlagSet("state", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var code, agent, source string
	flags.StringVar(&code, "workstream", "", "workstream code")
	flags.StringVar(&agent, "agent", "", "agent ID")
	flags.StringVar(&source, "source", "", "adapter")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 1 || code == "" || agent == "" || source == "" || (flags.Arg(0) != "working" && flags.Arg(0) != "idle") {
		return &publicError{message: usage}
	}
	if err := validateWorkstreamCode(code); err != nil {
		return err
	}
	credential, err := a.credentialFor(code, agent)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"state": flags.Arg(0), "source": source, "at": time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return &publicError{message: "Unable to prepare agent state."}
	}
	response, err := a.request(http.MethodPut, "/agent/v1/workstreams/"+code+"/agents/me/state", credential.APIToken, payload)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return workstreamResponseStatusError(response.status, response.body, code, true, credential)
	}
	return nil
}
