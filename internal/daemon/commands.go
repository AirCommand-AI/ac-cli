package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/AirCommand-AI/ac-cli/adapters/pi"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/machinectl"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

// Commands implements app.DaemonCommands. NewSupervisor is supplied by the
// executable, after the supervisor package is merged; tests inject a fake.
type Commands struct {
	Home          string
	Output        io.Writer
	Service       Service
	NewSupervisor func(tmux, pi string) (Supervisor, error)
	NewSocket     func(context.Context, Supervisor) (*SocketClient, error)
	NewControl    func(Supervisor) *machinectl.Control
}

func (c Commands) RunDaemon(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("Usage: aircom daemon start|stop|status")
	}
	ctx := context.Background()
	service := c.Service
	service.Home = c.Home
	switch arguments[0] {
	case "start":
		if len(arguments) != 1 {
			return errors.New("Usage: aircom daemon start")
		}
		if _, err := credentials.NewStore(c.Home).LoadMachine(); err != nil {
			return fmt.Errorf("machine is not registered; ask a person to run aircom init: %w", err)
		}
		return service.Start(ctx)
	case "stop":
		if len(arguments) != 1 {
			return errors.New("Usage: aircom daemon stop")
		}
		return service.Stop(ctx)
	case "status":
		if len(arguments) != 1 {
			return errors.New("Usage: aircom daemon status")
		}
		data, err := Call(ctx, c.Home, Request{Op: "status"})
		if err != nil {
			return err
		}
		return c.print(data)
	case "run":
		flags := flag.NewFlagSet("daemon run", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		tmux := flags.String("tmux", "", "")
		pi := flags.String("pi", "", "")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if len(flags.Args()) != 0 {
			return errors.New("Usage: aircom daemon run")
		}
		if c.NewSupervisor == nil {
			return errors.New("daemon supervisor is unavailable")
		}
		if err := piadapter.Sync(c.Home); err != nil {
			warn := fmt.Sprintf("warning: sync pi extension at daemon boot: %v (headless launches will refuse a stale extension)", err)
			if logErr := appendDaemonWarning(c.Home, warn); logErr != nil {
				log.Printf("%s; daemon log: %v", warn, logErr)
			}
		}
		supervisor, err := c.NewSupervisor(*tmux, *pi)
		if err != nil {
			return err
		}
		runCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer cancel()
		var socket *SocketClient
		if c.NewSocket != nil {
			socket, err = c.NewSocket(runCtx, supervisor)
			if err != nil {
				return err
			}
		}
		if c.NewControl != nil {
			if control := c.NewControl(supervisor); control != nil {
				if socket != nil {
					bindMachineControl(socket, control)
				} else {
					go func() {
						if ready, ok := supervisor.(interface{ WaitReady(context.Context) error }); ok {
							if err := ready.WaitReady(runCtx); err != nil {
								return
							}
						}
						control.Run(runCtx, func(err error) { log.Printf("machine control: %v", err) })
					}()
				}
			}
		}
		return Serve(runCtx, c.Home, supervisor, socket)
	default:
		return fmt.Errorf("unknown daemon command %q", strings.TrimSpace(arguments[0]))
	}
}

// One scheduler owns both the reconciler and reporter. The socket's
// content-free signal must trigger both, not a separate status-only loop.
func bindMachineControl(socket *SocketClient, control *machinectl.Control) {
	if socket.Control != nil {
		control.Reporter = socket.Control.Reporter
	}
	socket.Control = control
	socket.OnConnect = control.CheckIn
	socket.OnCheckIn = control.CheckIn
}

func appendDaemonWarning(home, warning string) error {
	if err := os.MkdirAll(storagepath.DaemonDirectory(home), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(storagepath.DaemonLog(home), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, warning)
	return err
}

func (c Commands) print(data json.RawMessage) error {
	out := c.Output
	if out == nil {
		out = os.Stdout
	}
	var pretty json.RawMessage
	if err := json.Unmarshal(data, &pretty); err != nil {
		return err
	}
	_, err := fmt.Fprintln(out, string(pretty))
	return err
}

// Paths are deliberately derived from the same storagepath helpers as the
// daemon, so a separate command process cannot target a different socket.
func (c Commands) SocketPath() string { return storagepath.DaemonSocket(c.Home) }
