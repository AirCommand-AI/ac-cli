package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	machineListUsage   = "Usage: aircom machine list --workstream <code> [--agent <id>] [--state all|active|ended] [--limit N] [--cursor <cursor>]"
	machineStatusUsage = "Usage: aircom machine status --workstream <code> [--agent <id>] --run <runId>"
	maxMachineRunPage  = 100
	machineRunNotFound = "Machine run not found"
)

var machineRunID = regexp.MustCompile(`^run_[0-9a-f]{24}$`)

// machineList prints one page of the workstream's machine runs, newest first.
// A page can hold fewer runs than --limit while nextCursor is set; keep paging.
func (a *App) machineList(args []string) error {
	flags := flag.NewFlagSet("machine list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var code, agent, state, cursor string
	var limit int
	flags.StringVar(&code, "workstream", "", "workstream code")
	flags.StringVar(&agent, "agent", "", "agent ID or name")
	flags.StringVar(&state, "state", "all", "all, active or ended")
	flags.IntVar(&limit, "limit", 20, "runs per page")
	flags.StringVar(&cursor, "cursor", "", "cursor from the previous page")
	if flags.Parse(args) != nil || flags.NArg() != 0 || code == "" {
		return &publicError{message: machineListUsage}
	}
	if err := validateWorkstreamCode(code); err != nil {
		return err
	}
	if state != "all" && state != "active" && state != "ended" {
		return &publicError{message: "--state must be all, active, or ended."}
	}
	if limit < 1 || limit > maxMachineRunPage {
		return &publicError{message: "--limit must be an integer from 1 to 100."}
	}
	query := url.Values{}
	query.Set("state", state)
	query.Set("limit", strconv.Itoa(limit))
	if cursor = strings.TrimSpace(cursor); cursor != "" {
		query.Set("cursor", cursor)
	}
	return a.machineRead(code, agent, "/agent/v1/workstreams/"+url.PathEscape(code)+"/machine-runs?"+query.Encode(), func(body []byte) bool {
		var page struct {
			Runs []json.RawMessage `json:"runs"`
		}
		return json.Unmarshal(body, &page) == nil && page.Runs != nil
	})
}

// machineStatus prints one machine run of the workstream: state, size, agents,
// repos, shutdown time (hardLimitAt) and, once ended, endedAt and endReason.
func (a *App) machineStatus(args []string) error {
	flags := flag.NewFlagSet("machine status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var code, agent, run string
	flags.StringVar(&code, "workstream", "", "workstream code")
	flags.StringVar(&agent, "agent", "", "agent ID or name")
	flags.StringVar(&run, "run", "", "run ID")
	if flags.Parse(args) != nil || flags.NArg() != 0 || code == "" || run == "" {
		return &publicError{message: machineStatusUsage}
	}
	if err := validateWorkstreamCode(code); err != nil {
		return err
	}
	if !machineRunID.MatchString(run) {
		return &publicError{message: "--run must be a run ID like run_0123456789abcdef01234567."}
	}
	return a.machineRead(code, agent, "/agent/v1/workstreams/"+url.PathEscape(code)+"/machine-runs/"+run, func(body []byte) bool {
		var view struct {
			RunID string `json:"runId"`
		}
		return json.Unmarshal(body, &view) == nil && view.RunID == run
	})
}

func (a *App) machineRead(code, agent, path string, valid func([]byte) bool) error {
	credential, err := a.credentialFor(code, agent)
	if err != nil {
		return err
	}
	response, err := a.request(http.MethodGet, path, credential.APIToken, nil)
	if err != nil {
		return err
	}
	message := responseError(response.body)
	if response.status == http.StatusBadRequest || response.status == http.StatusNotFound && message == machineRunNotFound {
		return &publicError{message: fmt.Sprintf("Machine run request rejected (HTTP %d): %s", response.status, message)}
	}
	if response.status < 200 || response.status >= 300 {
		return workstreamResponseStatusError(response.status, response.body, code, false, credential)
	}
	if !valid(response.body) {
		return &publicError{message: "The workstream service returned an invalid machine run response."}
	}
	return writeSafeResponse(a.outputWriter(), response.body, credential.APIToken, credential.SocketKey)
}
