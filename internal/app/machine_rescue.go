package app

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const machineRescueUsage = "Usage: aircom machine rescue-opened --workspace <workspace> --workstream <code> --run <runId>"

// machineRescueOpened tells the workstream that this machine's owner is about
// to open a rescue shell on a run machine (ac-stack/runs/rescue.sh). It uses
// this machine's own credential, never an agent's.
func (a *App) machineRescueOpened(args []string) error {
	flags := flag.NewFlagSet("machine rescue-opened", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workspace, code, run string
	flags.StringVar(&workspace, "workspace", "", "workspace name or id, as shown by aircom workspaces")
	flags.StringVar(&workspace, "org", "", "alias for --workspace")
	flags.StringVar(&code, "workstream", "", "workstream code")
	flags.StringVar(&run, "run", "", "run ID")
	if flags.Parse(args) != nil || flags.NArg() != 0 || workspace == "" || code == "" || run == "" {
		return &publicError{message: machineRescueUsage}
	}
	if err := validateWorkstreamCode(code); err != nil {
		return err
	}
	if !machineRunID.MatchString(run) {
		return &publicError{message: "--run must be a run ID like run_0123456789abcdef01234567."}
	}
	machine, err := a.machineCredential()
	if err != nil {
		return err
	}
	organizationID, err := a.resolveOrganization(workspace)
	if err != nil {
		return err
	}
	previous := a.Organization
	a.Organization = organizationID
	defer func() { a.Organization = previous }()
	response, err := a.request(http.MethodPost, "/v1/workstreams/"+url.PathEscape(code)+"/machine-runs/"+run+"/rescue-opened", machine.APIToken, nil)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return &publicError{message: fmt.Sprintf("Rescue notice rejected (HTTP %d): %s", response.status, responseError(response.body))}
	}
	_, err = fmt.Fprintf(a.outputWriter(), "Workstream %s told: rescue shell opened on %s.\n", code, run)
	return err
}
