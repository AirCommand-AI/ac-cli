package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/AirCommand-AI/ac-cli/internal/secrets"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/AirCommand-AI/ac-cli/internal/daemonclient"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

const agentUsage = "Usage: aircom agent create <name> [--mode headless|tmux] | remove <name> | start <name> --org <org> --workstream <code> [--mode headless|tmux] [--repo owner/repo]... | mode <name> headless|tmux | stop <name> | list | attach <name> | takeover <name> | interrupt <name> --message <text>"

// runAgent implements the command contract without depending on daemon service code.
func (a *App) runAgent(args []string) error {
	if len(args) == 0 {
		return &publicError{message: agentUsage}
	}
	client := daemonclient.Client{SocketPath: storagepath.DaemonSocket(a.Store.Home())}
	ctx := context.Background()
	switch args[0] {
	case "create":
		if len(args) != 2 && len(args) != 4 || !daemonclient.ValidAgentName(args[1]) {
			return &publicError{message: agentUsage}
		}
		mode := "headless"
		if len(args) == 4 {
			if args[2] != "--mode" || args[3] != "headless" && args[3] != "tmux" {
				return &publicError{message: agentUsage}
			}
			mode = args[3]
		}
		if err := a.connect([]string{"--name", args[1]}); err != nil {
			return err
		}
		agent, err := a.resolveAgent(args[1])
		if err != nil {
			return err
		}
		dir, err := storagepath.EnsureAgentDirectory(a.Store.Home(), agent.AgentID)
		if err != nil {
			return err
		}
		path := filepath.Join(dir, "mode")
		if err := os.WriteFile(path, []byte(mode), 0600); err != nil {
			return err
		}
		return nil
	case "remove":
		if len(args) != 2 || !daemonclient.ValidAgentName(args[1]) {
			return &publicError{message: agentUsage}
		}
		agent, err := a.resolveAgent(args[1])
		if err != nil {
			return err
		}
		// A stopped daemon has no socket, but its saved agent state must
		// still be forgotten before a later daemon restart.
		offline := false
		if err := client.Remove(ctx, args[1]); err != nil {
			if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ECONNREFUSED) {
				return &publicError{message: fmt.Sprintf("Unable to stop agent: %v", err)}
			}
			offline = true
		}
		if err := a.disconnect([]string{"--agent", args[1]}); err != nil {
			return err
		}
		if offline {
			for _, path := range []string{storagepath.AgentDaemonState(a.Store.Home(), agent.AgentID), storagepath.AgentBrief(a.Store.Home(), agent.AgentID), storagepath.AgentDelivered(a.Store.Home(), agent.AgentID)} {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return &publicError{message: fmt.Sprintf("Agent removed, but unable to delete %s: %v", path, err)}
				}
			}
		}
		return nil
	case "start":
		return a.startAgent(ctx, client, args[1:])
	case "mode":
		if len(args) != 3 || !daemonclient.ValidAgentName(args[1]) || args[2] != "headless" && args[2] != "tmux" {
			return &publicError{message: agentUsage}
		}
		err := client.Mode(ctx, args[1], args[2])
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			err = a.offlineAgentMode(args[1], args[2])
		}
		if err != nil {
			return &publicError{message: fmt.Sprintf("Unable to change agent mode: %v", err)}
		}
		fmt.Fprintf(a.outputWriter(), "Set %s mode to %s.\n", args[1], args[2])
		return nil
	case "stop":
		if len(args) != 2 || !daemonclient.ValidAgentName(args[1]) {
			return &publicError{message: agentUsage}
		}
		if err := client.Stop(ctx, args[1]); err != nil {
			return &publicError{message: fmt.Sprintf("Unable to stop agent: %v", err)}
		}
		fmt.Fprintf(a.outputWriter(), "Stopped %s.\n", args[1])
		return nil
	case "list":
		if len(args) != 1 {
			return &publicError{message: agentUsage}
		}
		agents, err := client.List(ctx)
		if err != nil {
			return &publicError{message: fmt.Sprintf("Unable to list agents: %v", err)}
		}
		for _, agent := range agents {
			if agent.Mode == "headless" {
				fmt.Fprintf(a.outputWriter(), "%s\t%s\t%s\t%s\t%s\t%s\n", agent.Name, agent.AgentID, agent.State, agent.Workstream, agent.Mode, agent.PiState)
			} else {
				fmt.Fprintf(a.outputWriter(), "%s\t%s\t%s\t%s\n", agent.Name, agent.AgentID, agent.State, agent.Workstream)
			}
		}
		return nil
	case "interrupt":
		if len(args) != 4 || !daemonclient.ValidAgentName(args[1]) || args[2] != "--message" || strings.TrimSpace(args[3]) == "" || len([]rune(args[3])) > 500 {
			return &publicError{message: agentUsage}
		}
		if err := a.interruptAgent(args[1], args[3]); err != nil {
			return err
		}
		fmt.Fprintf(a.outputWriter(), "Interrupt sent to %s.\n", args[1])
		return nil
	case "takeover":
		if len(args) != 2 || !daemonclient.ValidAgentName(args[1]) {
			return &publicError{message: agentUsage}
		}
		return client.Takeover(ctx, args[1], a.inputReader(), a.outputWriter(), a.errorWriter())
	case "attach":
		if len(args) != 2 || !daemonclient.ValidAgentName(args[1]) {
			return &publicError{message: agentUsage}
		}
		agents, err := client.List(ctx)
		if err != nil {
			return err
		}
		for _, agent := range agents {
			if agent.Name == args[1] && agent.Mode == "headless" {
				return client.Attach(ctx, args[1], a.inputReader(), a.outputWriter(), func(text string) error { return a.interruptAgent(args[1], text) }, func(text string) error {
					return a.postAttachUpdate(agent.Workstream, agent.AgentID, text)
				})
			}
		}
		command := exec.Command("tmux", "-L", "aircom", "attach", "-t", args[1])
		command.Stdin = a.inputReader()
		command.Stdout = a.outputWriter()
		command.Stderr = a.errorWriter()
		return command.Run()
	default:
		return &publicError{message: agentUsage}
	}
}

// postAttachUpdate keeps the normal update API and validation but only shows
// the confirmation line in the shared attach view, not the full metadata block.
func (a *App) postAttachUpdate(workstream, agentID, text string) error {
	machine, err := a.machineCredential()
	if err != nil {
		return err
	}
	label := strings.TrimSpace(machine.MachineName)
	if label == "" {
		label = machineName()
	}
	if label == "" {
		return &publicError{message: "Unable to determine this machine's name."}
	}
	var confirmation bytes.Buffer
	local := *a
	local.Stdout = &confirmation
	if err := local.update([]string{"--workstream", workstream, "--agent", agentID, "--body", "Operator (attach on " + label + "): " + text}); err != nil {
		return err
	}
	line, _, _ := strings.Cut(confirmation.String(), "\n")
	_, err = fmt.Fprintf(a.outputWriter(), "[AirCommand] %s\n", line)
	return err
}

func (a *App) interruptAgent(name, text string) error {
	if strings.TrimSpace(text) == "" || len([]rune(text)) > 500 {
		return &publicError{message: "Interrupt message must contain 1–500 characters."}
	}
	agent, err := a.resolveAgent(name)
	if err != nil {
		return err
	}
	credential, err := a.Store.FindByAgent(agent.WorkstreamCode, agent.AgentID)
	if err != nil {
		return err
	}
	if credential.OrganizationID == "" {
		return &publicError{message: "Agent organization is missing from its credential."}
	}
	machine, err := a.machineCredential()
	if err != nil {
		return err
	}
	id, err := secrets.IdempotencyID(a.randomReader())
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{"text": text, "idempotencyId": id})
	if err != nil {
		return err
	}
	previous := a.Organization
	a.Organization = credential.OrganizationID
	defer func() { a.Organization = previous }()
	result, err := a.request(http.MethodPost, "/agent/v1/machines/me/agents/"+url.PathEscape(agent.AgentID)+"/interrupt", machine.APIToken, payload)
	if err != nil {
		return err
	}
	if result.status < 200 || result.status >= 300 {
		var failure struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Error   struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(result.body, &failure)
		code, message := failure.Code, failure.Message
		if code == "" {
			code = failure.Error.Code
		}
		if message == "" {
			message = failure.Error.Message
		}
		if code == "" {
			code = fmt.Sprintf("HTTP %d", result.status)
		}
		return &publicError{message: fmt.Sprintf("AirCommand interrupt failed: %s: %s", code, message)}
	}
	return nil
}

type repoFlags []string

func (r *repoFlags) String() string         { return strings.Join(*r, ",") }
func (r *repoFlags) Set(value string) error { *r = append(*r, value); return nil }

func (a *App) startAgent(ctx context.Context, client daemonclient.Client, args []string) error {
	if len(args) == 0 || !daemonclient.ValidAgentName(args[0]) {
		return &publicError{message: agentUsage}
	}
	name := args[0]
	flags := flag.NewFlagSet("agent start", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var org, code, mode string
	flags.StringVar(&mode, "mode", "", "pi mode (headless or tmux)")
	var repos repoFlags
	flags.StringVar(&org, "org", "", "organization name or ID")
	flags.StringVar(&code, "workstream", "", "workstream code")
	flags.Var(&repos, "repo", "GitHub owner/repo")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || strings.TrimSpace(org) == "" || code == "" {
		return &publicError{message: agentUsage}
	}
	if mode != "" && mode != "headless" && mode != "tmux" {
		return &publicError{message: agentUsage}
	}
	if err := validateWorkstreamCode(code); err != nil {
		return err
	}
	home := a.Store.Home()
	folder, err := (daemonclient.Workspace{Root: filepath.Join(home, "work")}).Prepare(name, repos)
	if err != nil {
		return &publicError{message: fmt.Sprintf("Unable to prepare work folder: %v", err)}
	}
	agents, err := a.fetchAgents()
	if err != nil {
		return err
	}
	exists := false
	for _, agent := range agents {
		if agent.Name == name && agent.Status != agentRetired {
			exists = true
			break
		}
	}
	if !exists {
		if err := a.connect([]string{"--name", name}); err != nil {
			return err
		}
	}
	agent, err := a.resolveAgent(name)
	if err != nil {
		return err
	}
	a.joinForDaemon = true
	err = a.join([]string{"--agent", agent.AgentID, "--org", org, "--workstream", code})
	a.joinForDaemon = false
	if err != nil {
		return err
	}
	if err := daemonclient.WriteBrief(storagepath.AgentBrief(home, agent.AgentID), name, org, code); err != nil {
		return &publicError{message: fmt.Sprintf("Unable to write agent brief: %v", err)}
	}
	if mode == "" {
		if _, err := os.Stat(storagepath.AgentDaemonState(home, agent.AgentID)); errors.Is(err, os.ErrNotExist) {
			mode = "headless"
			if preference, err := os.ReadFile(filepath.Join(storagepath.AgentDirectory(home, agent.AgentID), "mode")); err == nil {
				mode = strings.TrimSpace(string(preference))
			}
		} else if err != nil {
			return err
		}
	}
	if err := client.Start(ctx, daemonclient.StartRequest{Name: name, AgentID: agent.AgentID, Organization: org, Workstream: code, Repos: repos, WorkFolder: folder, Mode: mode}); err != nil {
		return &publicError{message: fmt.Sprintf("Unable to start agent: %v", err)}
	}
	fmt.Fprintf(a.outputWriter(), "Started %s.\n", name)
	return nil
}

// A stopped daemon cannot edit its definitions; the CLI may edit only a
// stopped agent, with the same atomic replacement and file permissions.
func (a *App) offlineAgentMode(name, mode string) error {
	agent, err := a.resolveAgent(name)
	if err != nil {
		return err
	}
	path := storagepath.AgentDaemonState(a.Store.Home(), agent.AgentID)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var def supervisor.AgentDefinition
	if err := json.Unmarshal(data, &def); err != nil {
		return err
	}
	if def.AgentID != agent.AgentID || def.Name != name || def.Desired != "stopped" {
		return fmt.Errorf("agent must be stopped before changing mode")
	}
	def.Mode = mode
	f, err := os.CreateTemp(filepath.Dir(path), ".daemon-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if err := json.NewEncoder(f).Encode(def); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
