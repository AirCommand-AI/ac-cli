package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CommandTmux uses a dedicated tmux socket. Args are never passed through a shell.
type CommandTmux struct{ Path string }

func (t CommandTmux) command(ctx context.Context, args ...string) *exec.Cmd {
	path := t.Path
	if path == "" {
		path = "tmux"
	}
	return exec.CommandContext(ctx, path, append([]string{"-L", "aircom"}, args...)...)
}
func (t CommandTmux) Inspect(ctx context.Context, name string) (Pane, error) {
	if !validName(name) {
		return Pane{}, fmt.Errorf("invalid agent name")
	}
	out, err := t.command(ctx, "list-panes", "-t", name+":0", "-F", "#{pane_dead}:#{pane_dead_status}:#{pane_dead_signal}:#{pane_pid}").CombinedOutput()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return Pane{}, fmt.Errorf("tmux is required to launch a daemon-started agent; install tmux: %w", err)
		}
		// No tmux server or session is not an error; other failures are.
		text := string(out)
		if strings.Contains(text, "no server running") || strings.Contains(text, "can't find session") || strings.Contains(text, "no sessions") || strings.Contains(text, "error connecting to") {
			return Pane{}, nil
		}
		return Pane{}, fmt.Errorf("inspect tmux pane: %w: %s", err, text)
	}
	return parsePaneOutput(string(out))
}

func parsePaneOutput(output string) (Pane, error) {
	fields := strings.Split(strings.TrimSpace(output), ":")
	if len(fields) != 4 || fields[0] != "0" && fields[0] != "1" {
		return Pane{}, fmt.Errorf("invalid tmux pane output")
	}
	dead := fields[0] == "1"
	code := 0
	signal := ""
	if dead {
		signal = fields[2]
		if fields[1] == "" {
			code = -1
			// Older tmux versions leave pane_dead_signal empty even after SIGTERM.
			if signal == "" {
				signal = "unknown"
			}
		} else {
			var err error
			code, err = strconv.Atoi(fields[1])
			if err != nil {
				return Pane{}, fmt.Errorf("invalid tmux pane exit status: %w", err)
			}
		}
	}
	pid, err := strconv.Atoi(fields[3])
	if err != nil || pid <= 0 {
		return Pane{}, fmt.Errorf("invalid tmux pane PID")
	}
	return Pane{Exists: true, Dead: dead, ExitCode: code, Signal: signal, PID: pid}, nil
}

// Activity reads only the dedicated aircom tmux socket. window_activity is
// tmux's Unix timestamp of the most recent pane output in that window.
func (t CommandTmux) Activity(ctx context.Context, names []string) (bool, time.Time, error) {
	clients, err := t.command(ctx, "list-clients", "-F", "#{client_session}").CombinedOutput()
	if err != nil && !tmuxMissing(string(clients)) {
		return false, time.Time{}, fmt.Errorf("inspect tmux clients: %w: %s", err, clients)
	}
	attached := strings.TrimSpace(string(clients)) != "" && err == nil
	var last time.Time
	for _, name := range names {
		if !validName(name) {
			return false, time.Time{}, fmt.Errorf("invalid agent name")
		}
		out, err := t.command(ctx, "list-windows", "-t", "="+name, "-F", "#{window_activity}").CombinedOutput()
		if err != nil {
			if tmuxMissing(string(out)) {
				continue
			}
			return false, time.Time{}, fmt.Errorf("inspect tmux activity: %w: %s", err, out)
		}
		for _, line := range strings.Fields(string(out)) {
			seconds, err := strconv.ParseInt(line, 10, 64)
			if err != nil || seconds < 0 {
				return false, time.Time{}, fmt.Errorf("invalid tmux window_activity %q", line)
			}
			when := time.Unix(seconds, 0)
			if when.After(last) {
				last = when
			}
		}
	}
	return attached, last, nil
}
func tmuxMissing(output string) bool {
	return strings.Contains(output, "no server running") || strings.Contains(output, "can't find session") || strings.Contains(output, "no sessions") || strings.Contains(output, "error connecting to")
}
func (t CommandTmux) Start(ctx context.Context, def AgentDefinition, args []string) error {
	if !validName(def.Name) {
		return fmt.Errorf("invalid agent name")
	}
	// Create a temporary shell pane, set remain-on-exit, then replace it
	// with pi. An immediately exiting pi cannot destroy its session.
	if out, err := t.command(ctx, "new-session", "-d", "-s", def.Name, "-n", "pi", "-c", def.WorkFolder).CombinedOutput(); err != nil {
		return fmt.Errorf("start tmux: %w: %s", err, out)
	}
	if out, err := t.command(ctx, "set-option", "-t", def.Name+":0", "remain-on-exit", "on").CombinedOutput(); err != nil {
		_ = t.Kill(ctx, def.Name)
		return fmt.Errorf("set tmux remain-on-exit: %w: %s", err, out)
	}
	// tmux respawn-pane takes one shell-command string. Quote every argument.
	if out, err := t.command(ctx, "respawn-pane", "-k", "-t", def.Name+":0.0", "-c", def.WorkFolder, shellJoin(args)).CombinedOutput(); err != nil {
		_ = t.Kill(ctx, def.Name)
		return fmt.Errorf("launch pi: %w: %s", err, out)
	}
	return nil
}
func (t CommandTmux) Kill(ctx context.Context, name string) error {
	if !validName(name) {
		return fmt.Errorf("invalid agent name")
	}
	pane, err := t.Inspect(ctx, name)
	if err != nil || !pane.Exists {
		return err
	}
	if out, err := t.command(ctx, "kill-session", "-t", "="+name).CombinedOutput(); err != nil {
		return fmt.Errorf("kill tmux session: %w: %s", err, out)
	}
	return nil
}
func shellJoin(args []string) string {
	var quoted []string
	for _, arg := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", "'\\''")+"'")
	}
	return strings.Join(quoted, " ")
}
