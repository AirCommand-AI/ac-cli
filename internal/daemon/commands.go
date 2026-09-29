package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

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
		if *tmux == "" || *pi == "" || len(flags.Args()) != 0 {
			return errors.New("Usage: aircom daemon run --tmux <absolute-path> --pi <absolute-path>")
		}
		if c.NewSupervisor == nil {
			return errors.New("daemon supervisor is unavailable")
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
		return Serve(runCtx, c.Home, supervisor, socket)
	default:
		return fmt.Errorf("unknown daemon command %q", strings.TrimSpace(arguments[0]))
	}
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
