package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/daemonclient"
)

// listenDaemon only prints daemon notifications. The daemon owns the remote
// poll, lock, spool and cursor; this process cannot consume messages itself.
func (a *App) listenDaemon(workstream, agentID string) error {
	client := a.sessionControl()
	if err := a.ensureDaemon(client); err != nil {
		return err
	}
	process, err := discoverSession(os.Getpid(), a.ProcessSnapshot)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		detached := false
		err := client.SubscribeSession(context.Background(), process.SessionPID, func(msg daemonclient.SessionMessage) error {
			switch msg.Type {
			case "connect":
				// Joining without a workstream waits in the daemon until placement.
				if msg.AgentID != agentID {
					return fmt.Errorf("daemon connected the wrong agent")
				}
			case "wake":
				if _, err := fmt.Fprintln(a.outputWriter(), msg.Line); err != nil {
					return err
				}
				// Record what was printed so a restarted listener does not replay it.
				if err := client.AckSession(context.Background(), process.SessionPID, msg.Offset); err != nil {
					fmt.Fprintf(a.errorWriter(), "Could not record delivery with the daemon: %v\n", err)
				}
			case "nudge", "interrupt":
				if _, err := fmt.Fprintln(a.outputWriter(), msg.Text); err != nil {
					return err
				}
			case "detached":
				detached = true
				return nil
			}
			return nil
		})
		if detached {
			return nil
		}
		if err != nil && err != io.EOF {
			fmt.Fprintf(a.errorWriter(), "Daemon session stream interrupted: %v\n", err)
		}
		if a.ListenPollLimit > 0 && attempt+1 >= a.ListenPollLimit {
			return err
		}
		delay := time.Second * time.Duration(1<<min(attempt, 5))
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		if a.ListenSleep != nil {
			a.ListenSleep(delay)
		} else {
			time.Sleep(delay)
		}
	}
}
