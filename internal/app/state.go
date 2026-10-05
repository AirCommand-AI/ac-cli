package app

import (
	"context"
	"flag"
	"io"
	"os"
)

// state is adapter plumbing for programs without the pi add-on. The daemon is
// the only process that reports presence to AirCommand; this command only
// reports the caller's logical state over the local control socket.
func (a *App) state(arguments []string) error {
	const usage = "Usage: aircom state [--workstream <code>] [--agent <id>] <working|idle|waiting|no_activity|unknown> --source <name>"
	flags := flag.NewFlagSet("state", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var code, agent, source string
	flags.StringVar(&code, "workstream", "", "workstream code")
	flags.StringVar(&agent, "agent", "", "agent ID")
	flags.StringVar(&source, "source", "", "reporter")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 1 {
		return &publicError{message: usage}
	}
	logical := flags.Arg(0)
	switch logical {
	case "working", "idle", "waiting", "no_activity", "unknown":
	default:
		return &publicError{message: usage}
	}
	if code != "" {
		if err := validateWorkstreamCode(code); err != nil {
			return err
		}
	}
	_ = agent
	_ = source // Old adapter flags are accepted but cannot confer identity.
	process, err := discoverSession(os.Getpid(), a.ProcessSnapshot)
	if err != nil {
		return err
	}
	if err := a.sessionControl().SessionEvent(context.Background(), process.SessionPID, "state", logical); err != nil {
		return &publicError{message: "Unable to report state to the machine daemon: " + err.Error()}
	}
	return nil
}
