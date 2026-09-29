package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AirCommand-AI/ac-cli/internal/daemonclient"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

const agentUsage = "Usage: aircom agent create <name> | remove <name> | start <name> --org <org> --workstream <code> [--repo owner/repo]... | stop <name> | list | attach <name>"

// runAgent implements the command contract without depending on daemon service code.
func (a *App) runAgent(args []string) error {
	if len(args) == 0 {
		return &publicError{message: agentUsage}
	}
	client := daemonclient.Client{SocketPath: storagepath.DaemonSocket(a.Store.Home())}
	ctx := context.Background()
	switch args[0] {
	case "create":
		if len(args) != 2 || !daemonclient.ValidAgentName(args[1]) {
			return &publicError{message: agentUsage}
		}
		return a.connect([]string{"--name", args[1]})
	case "remove":
		if len(args) != 2 || !daemonclient.ValidAgentName(args[1]) {
			return &publicError{message: agentUsage}
		}
		if err := client.Remove(ctx, args[1]); err != nil {
			return &publicError{message: fmt.Sprintf("Unable to stop agent: %v", err)}
		}
		return a.disconnect([]string{"--agent", args[1]})
	case "start":
		return a.startAgent(ctx, client, args[1:])
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
			fmt.Fprintf(a.outputWriter(), "%s\t%s\t%s\t%s\n", agent.Name, agent.AgentID, agent.State, agent.Workstream)
		}
		return nil
	case "attach":
		if len(args) != 2 || !daemonclient.ValidAgentName(args[1]) {
			return &publicError{message: agentUsage}
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
	var org, code string
	var repos repoFlags
	flags.StringVar(&org, "org", "", "organization name or ID")
	flags.StringVar(&code, "workstream", "", "workstream code")
	flags.Var(&repos, "repo", "GitHub owner/repo")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || strings.TrimSpace(org) == "" || code == "" {
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
	if err := a.join([]string{"--agent", agent.AgentID, "--org", org, "--workstream", code}); err != nil {
		return err
	}
	if err := daemonclient.WriteBrief(storagepath.AgentBrief(home, agent.AgentID), name, org, code); err != nil {
		return &publicError{message: fmt.Sprintf("Unable to write agent brief: %v", err)}
	}
	if err := client.Start(ctx, daemonclient.StartRequest{Name: name, AgentID: agent.AgentID, Organization: org, Workstream: code, Repos: repos, WorkFolder: folder}); err != nil {
		return &publicError{message: fmt.Sprintf("Unable to start agent: %v", err)}
	}
	fmt.Fprintf(a.outputWriter(), "Started %s.\n", name)
	return nil
}
