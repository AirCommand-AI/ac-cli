package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/AirCommand-AI/ac-cli/internal/runmode"
)

const machineRunUsage = "Usage: aircom machine bootstrap --code-file <owner-only-file> | machine request --workstream <code> --agent <id> --profile <name> [--agents N] [--repo <owner/repo>]... [--runtime-min N] | machine done|cancel --workstream <code> --agent <id> --run <runId>"

func (a *App) machineRun(args []string) error {
	if len(args) == 0 {
		return &publicError{message: machineRunUsage}
	}
	if args[0] == "bootstrap" {
		flags := flag.NewFlagSet("machine bootstrap", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		var codeFile string
		flags.StringVar(&codeFile, "code-file", "", "owner-only start code file")
		if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || codeFile == "" || a.Store == nil {
			return &publicError{message: machineRunUsage}
		}
		if _, err := a.Store.LoadMachine(); err == nil {
			return &publicError{message: "Refusing bootstrap: this machine is already registered."}
		}
		run, err := (runmode.Bootstrap{Home: a.Store.Home(), BaseURL: a.BaseURL, MetadataURL: a.BootstrapMetadataURL, Client: a.HTTPClient, Random: a.Random}).Exchange(a.commandContext(), codeFile)
		if err != nil {
			return &publicError{message: "Unable to bootstrap run machine: " + err.Error()}
		}
		if err = a.daemonCommand([]string{"start"}); err != nil {
			return err
		}
		fmt.Fprintf(a.outputWriter(), "Run %s bootstrapped; daemon running.\n", run.RunID)
		return nil
	}
	if args[0] != "request" && args[0] != "done" && args[0] != "cancel" {
		return &publicError{message: machineRunUsage}
	}
	flags := flag.NewFlagSet("machine "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var code, agent, profile, run string
	var agents, runtimeMin int
	var repos stringList
	flags.StringVar(&code, "workstream", "", "workstream code")
	flags.StringVar(&agent, "agent", "", "agent ID or name")
	flags.StringVar(&profile, "profile", "", "machine profile")
	flags.StringVar(&run, "run", "", "run ID")
	flags.IntVar(&agents, "agents", 1, "agent count")
	flags.IntVar(&runtimeMin, "runtime-min", 0, "run limit in minutes")
	flags.Var(&repos, "repo", "repo (repeatable)")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || code == "" || validateWorkstreamCode(code) != nil {
		return &publicError{message: machineRunUsage}
	}
	if args[0] == "request" && (profile == "" || run != "" || agents < 1 || agents > 32 || runtimeMin < 0 || runtimeMin > 720) {
		return &publicError{message: machineRunUsage}
	}
	if args[0] != "request" && (run == "" || profile != "" || len(repos.values) > 0 || runtimeMin != 0 || agents != 1) {
		return &publicError{message: machineRunUsage}
	}
	credential, err := a.credentialFor(code, agent)
	if err != nil {
		return err
	}
	path := "/agent/v1/workstreams/" + url.PathEscape(code) + "/machine-runs"
	var payload []byte
	if args[0] == "request" {
		payload, _ = json.Marshal(map[string]any{"profile": profile, "agents": agents, "repos": repos.values, "runtimeLimitMin": runtimeMin})
	} else {
		path += "/" + url.PathEscape(run) + "/" + args[0]
		payload = []byte("{}")
	}
	response, err := a.request(http.MethodPost, path, credential.APIToken, payload)
	if err != nil {
		return err
	}
	if response.status < 200 || response.status >= 300 {
		return &publicError{message: fmt.Sprintf("Machine run request rejected (HTTP %d).", response.status)}
	}
	if args[0] == "request" {
		var created struct {
			RunID string `json:"runId"`
		}
		if json.Unmarshal(response.body, &created) != nil || created.RunID == "" {
			return &publicError{message: "Invalid machine run response."}
		}
		return a.writeActionLine("Machine run requested: " + singleLine(created.RunID))
	}
	return a.writeActionLine("Machine run " + args[0] + " requested: " + singleLine(run))
}

func (a *App) commandContext() context.Context { return context.Background() }

// The daemon owns the hourly installation token and responds to this helper.
// See internal/runmode for the user-only socket protocol.
func (a *App) gitCredential(args []string) error {
	if len(args) != 1 || args[0] != "get" {
		return &publicError{message: "Usage: aircom git-credential get"}
	}
	if a.Store == nil {
		return &publicError{message: "Machine credential unavailable."}
	}
	if _, err := runmode.Load(a.Store.Home()); err != nil {
		return &publicError{message: "git-credential is available only on a run machine"}
	}
	return &publicError{message: "git-credential daemon token helper is not configured"}
}
