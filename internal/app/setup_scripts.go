package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A workstream's setup scripts install tools on a new cloud machine, as root,
// before its agents start. `machine start --setup-script <name>` picks one.

const setupScriptLimit = 10_000

const setupScriptsUsage = "Usage: aircom setup-scripts --workstream <code> [--agent <agentId|name>] | setup-script get <name> --workstream <code> [--agent <agentId|name>] [--out <file>] | setup-script put <name> --workstream <code> [--agent <agentId|name>] [--file <path>] (reads standard input without --file) | setup-script delete <name> --workstream <code> [--agent <agentId|name>]"

type setupScript struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Script    string `json:"script"`
	UpdatedAt string `json:"updatedAt"`
	UpdatedBy string `json:"updatedBy"`
}

func validSetupScriptName(name string) bool {
	trimmed := strings.TrimSpace(name)
	return trimmed != "" && trimmed == name && utf8.RuneCountInString(name) <= 60 && strings.IndexFunc(name, unicode.IsControl) < 0
}

func (a *App) setupScriptRequest(workstream, agent, method, path string, payload any) (httpResult, error) {
	cred, err := a.credentialFor(workstream, agent)
	if err != nil {
		return httpResult{}, err
	}
	var body []byte
	if payload != nil {
		if body, err = json.Marshal(payload); err != nil {
			return httpResult{}, err
		}
	}
	response, err := a.request(method, "/agent/v1/workstreams/"+url.PathEscape(workstream)+"/setup-scripts"+path, cred.APIToken, body)
	if err != nil {
		return httpResult{}, err
	}
	if response.status == http.StatusBadRequest || response.status == http.StatusConflict {
		return httpResult{}, &publicError{message: "Setup script rejected: " + responseError(response.body)}
	}
	if response.status < 200 || response.status >= 300 {
		return httpResult{}, workstreamResponseStatusError(response.status, response.body, workstream, method != http.MethodGet, cred)
	}
	return response, nil
}

func (a *App) listSetupScripts(workstream, agent string) ([]setupScript, error) {
	response, err := a.setupScriptRequest(workstream, agent, http.MethodGet, "", nil)
	if err != nil {
		return nil, err
	}
	var listed struct {
		Scripts []setupScript `json:"scripts"`
	}
	if json.Unmarshal(response.body, &listed) != nil {
		return nil, &publicError{message: "Invalid setup scripts response."}
	}
	return listed.Scripts, nil
}

// findSetupScript matches a name the way the dashboard does: ignoring case.
func findSetupScript(scripts []setupScript, name string) *setupScript {
	for i := range scripts {
		if strings.EqualFold(scripts[i].Name, name) {
			return &scripts[i]
		}
	}
	return nil
}

func (a *App) setupScripts(arguments []string) error {
	flags := flag.NewFlagSet("setup-scripts", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstream, agent string
	flags.StringVar(&workstream, "workstream", "", "workstream code")
	flags.StringVar(&agent, "agent", "", "agent")
	if flags.Parse(arguments) != nil || flags.NArg() != 0 || validateWorkstreamCode(workstream) != nil {
		return &publicError{message: setupScriptsUsage}
	}
	scripts, err := a.listSetupScripts(workstream, agent)
	if err != nil {
		return err
	}
	if len(scripts) == 0 {
		return a.writeActionLine("No setup scripts in workstream " + workstream + ".")
	}
	var out strings.Builder
	for _, script := range scripts {
		lines := len(strings.Split(strings.TrimSpace(script.Script), "\n"))
		fmt.Fprintf(&out, "%s\t%d lines\tchanged by %s at %s\n", singleLine(script.Name), lines, singleLine(script.UpdatedBy), singleLine(script.UpdatedAt))
	}
	return writeSafeResponse(a.outputWriter(), []byte(out.String()))
}

func (a *App) setupScript(arguments []string) error {
	if len(arguments) < 2 {
		return &publicError{message: setupScriptsUsage}
	}
	action, name := arguments[0], arguments[1]
	if !validSetupScriptName(name) {
		return &publicError{message: "Setup script name must be 1–60 characters with no leading or trailing spaces."}
	}
	flags := flag.NewFlagSet("setup-script", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var workstream, agent, file, out string
	flags.StringVar(&workstream, "workstream", "", "workstream code")
	flags.StringVar(&agent, "agent", "", "agent")
	flags.StringVar(&file, "file", "", "script file")
	flags.StringVar(&out, "out", "", "output file")
	if flags.Parse(arguments[2:]) != nil || flags.NArg() != 0 || validateWorkstreamCode(workstream) != nil || file != "" && action != "put" || out != "" && action != "get" {
		return &publicError{message: setupScriptsUsage}
	}
	switch action {
	case "get", "put", "delete":
	default:
		return &publicError{message: setupScriptsUsage}
	}
	scripts, err := a.listSetupScripts(workstream, agent)
	if err != nil {
		return err
	}
	found := findSetupScript(scripts, name)
	if found == nil && action != "put" {
		return &publicError{message: fmt.Sprintf("Workstream %s has no setup script named %q.", workstream, name)}
	}
	switch action {
	case "get":
		if out != "" {
			if err = os.WriteFile(out, []byte(found.Script), 0600); err != nil {
				return &publicError{message: "Unable to write setup script output."}
			}
			return nil
		}
		if _, err = io.WriteString(a.outputWriter(), found.Script); err != nil {
			return &publicError{message: "Unable to print setup script."}
		}
		return nil
	case "delete":
		if _, err = a.setupScriptRequest(workstream, agent, http.MethodDelete, "/"+url.PathEscape(found.ID), nil); err != nil {
			return err
		}
		return a.writeActionLine("Setup script deleted: " + singleLine(found.Name))
	}
	content, err := a.readSetupScript(file)
	if err != nil {
		return err
	}
	payload := map[string]string{"name": name, "script": content}
	if found == nil {
		_, err = a.setupScriptRequest(workstream, agent, http.MethodPost, "", payload)
		if err != nil {
			return err
		}
		return a.writeActionLine("Setup script created: " + singleLine(name))
	}
	if _, err = a.setupScriptRequest(workstream, agent, http.MethodPut, "/"+url.PathEscape(found.ID), payload); err != nil {
		return err
	}
	return a.writeActionLine("Setup script updated: " + singleLine(name))
}

// readSetupScript reads --file, or standard input when no file is given.
func (a *App) readSetupScript(file string) (string, error) {
	var source io.Reader = a.inputReader()
	if file != "" {
		f, err := os.Open(filepath.Clean(file))
		if err != nil {
			return "", &publicError{message: "Unable to read setup script file."}
		}
		defer f.Close()
		source = f
	}
	content, err := io.ReadAll(io.LimitReader(source, setupScriptLimit+1))
	if err != nil {
		return "", &publicError{message: "Unable to read setup script."}
	}
	if len(content) > setupScriptLimit {
		return "", &publicError{message: fmt.Sprintf("Setup script exceeds %d characters.", setupScriptLimit)}
	}
	if strings.TrimSpace(string(content)) == "" || !utf8.Valid(content) {
		return "", &publicError{message: "Setup script must be non-empty plain text."}
	}
	return string(content), nil
}
