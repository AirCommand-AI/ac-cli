package main

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/app"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/daemon"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
	"github.com/AirCommand-AI/ac-cli/internal/machinectl"
	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

const dashboardURL = "https://dashboard.aircommand.ai"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		if err := writeVersion(os.Stdout); err != nil {
			_, _ = os.Stderr.WriteString("Unable to write version output.\n")
			os.Exit(1)
		}
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		_, _ = os.Stderr.WriteString("Unable to locate the home directory.\n")
		os.Exit(1)
	}

	cliPath, err := os.Executable()
	if err != nil {
		_, _ = os.Stderr.WriteString("Unable to locate the aircom executable.\n")
		os.Exit(1)
	}
	store := credentials.NewStore(home)
	httpClient := &http.Client{Timeout: 30 * time.Second}
	daemon.AircomVersion = version
	commands := daemon.Commands{Home: home, Output: os.Stdout}
	operationGate := &sync.Mutex{}
	commands.NewSupervisor = func(tmux, pi string) (daemon.Supervisor, error) {
		if tmux == "" {
			tmux = "tmux"
		}
		if pi == "" {
			pi = "pi"
		}
		poll := &supervisor.HTTPPoller{BaseURL: dashboardURL, Client: httpClient, Store: store}
		manager := supervisor.New(home, pi, cliPath, supervisor.CommandTmux{Path: tmux}, poll)
		manager.OperationGate = operationGate
		api := machinectl.HTTPAPI{BaseURL: dashboardURL, Client: httpClient, Store: store}
		manager.DesiredPost = func(ctx context.Context, d supervisor.AgentDefinition) (int64, error) {
			if d.Revision == 0 {
				cred, err := store.FindByAgent(d.Workstream, d.AgentID)
				if err != nil {
					return 0, err
				}
				if err := api.Seed(ctx, []machinectl.Seed{{AgentID: d.AgentID, Desired: d.Desired, Mode: d.Mode, Repos: d.Repos, WorkFolder: d.WorkFolder, AssignedOrganizationID: cred.OrganizationID, AssignedWorkstreamCode: d.Workstream}}); err != nil {
					return 0, err
				}
			}
			return api.Desired(ctx, d.AgentID, d.Desired, d.Mode)
		}
		return manager, nil
	}
	commands.NewControl = func(manager daemon.Supervisor) *machinectl.Control {
		return machinectl.New(&machinectl.AgentReconciler{
			API:     machinectl.HTTPAPI{BaseURL: dashboardURL, Client: httpClient, Store: store},
			Manager: manager.(machinectl.Manager), Store: store, Home: home, Gate: operationGate,
		}, nil)
	}
	commands.NewSocket = func(ctx context.Context, manager daemon.Supervisor) (*daemon.SocketClient, error) {
		machine, err := store.LoadMachine()
		if err != nil {
			return nil, nil
		} // offline polling still runs without a machine socket
		source := manager.(machinectl.StatusSource)
		control := machinectl.New(nil, &machinectl.HTTPReporter{URL: dashboardURL, Token: machine.APIToken, Version: version, Client: httpClient, Source: source})
		return &daemon.SocketClient{URL: "wss://ac.aircommand.ai/machine", MachineID: machine.DeviceID, Sink: manager.(daemon.WakeSink), Control: control, OnCheckIn: control.CheckIn, OnConnect: control.CheckIn, SecretLoader: func(ctx context.Context) (string, error) {
			return daemon.MachineSecret(ctx, store, httpClient, dashboardURL)
		}}, nil
	}
	client := &app.App{
		BaseURL:        dashboardURL,
		HTTPClient:     httpClient,
		Store:          store,
		ListenStore:    listenstore.NewStore(home),
		Stdin:          os.Stdin,
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
		DaemonCommands: commands,
	}
	os.Exit(client.Run(os.Args[1:]))
}
