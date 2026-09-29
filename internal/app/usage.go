package app

import (
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"strconv"
)

const usageReportUsage = "Usage: aircom usage --workstream <code> [--agent <agentId|name>] --turn <id> --input <tokens> --output <tokens>"

func (a *App) usageReport(args []string) error {
	flags := flag.NewFlagSet("usage", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var code, agent, turn string
	var input, output int64
	flags.StringVar(&code, "workstream", "", "workstream")
	flags.StringVar(&agent, "agent", "", "agent")
	flags.StringVar(&turn, "turn", "", "stable turn ID")
	flags.Int64Var(&input, "input", 0, "input tokens")
	flags.Int64Var(&output, "output", 0, "output tokens")
	if flags.Parse(args) != nil || flags.NArg() != 0 || code == "" || turn == "" || input < 0 || output < 0 || input == 0 && output == 0 {
		return &publicError{message: usageReportUsage}
	}
	if err := validateWorkstreamCode(code); err != nil {
		return err
	}
	credential, err := a.credentialFor(code, agent)
	if err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"turnId": turn, "tokensIn": input, "tokensOut": output})
	if err != nil {
		return &publicError{message: "Unable to encode token usage."}
	}
	response, err := a.request(http.MethodPost, "/agent/v1/workstreams/"+code+"/usage", credential.APIToken, data)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return workstreamResponseStatusError(response.status, response.body, code, false, credential)
	}
	_, err = a.outputWriter().Write([]byte("Recorded turn usage: " + strconv.FormatInt(input, 10) + " in, " + strconv.FormatInt(output, 10) + " out.\n"))
	return err
}
