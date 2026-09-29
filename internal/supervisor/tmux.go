package supervisor

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// CommandTmux uses a dedicated tmux socket. Args are never passed through a shell.
type CommandTmux struct{ Path string }

func (t CommandTmux) command(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, t.Path, append([]string{"-L", "aircom"}, args...)...)
}
func (t CommandTmux) Inspect(ctx context.Context, name string) (Pane, error) {
	if !validName(name) {
		return Pane{}, fmt.Errorf("invalid agent name")
	}
	out, err := t.command(ctx, "list-panes", "-t", name+":0", "-F", "#{pane_dead} #{pane_dead_status} #{pane_pid}").CombinedOutput()
	if err != nil {
		// No tmux server or session is not an error; other failures are.
		text := string(out)
		if strings.Contains(text, "no server running") || strings.Contains(text, "can't find session") || strings.Contains(text, "no sessions") || strings.Contains(text, "error connecting to") {
			return Pane{}, nil
		}
		return Pane{}, fmt.Errorf("inspect tmux pane: %w: %s", err, text)
	}
	fields := strings.Fields(string(out))
	if len(fields) != 3 {
		return Pane{}, fmt.Errorf("invalid tmux pane output")
	}
	code, err := strconv.Atoi(fields[1])
	if err != nil {
		return Pane{}, err
	}
	pid, err := strconv.Atoi(fields[2])
	if err != nil {
		return Pane{}, err
	}
	return Pane{Exists: true, Dead: fields[0] == "1", ExitCode: code, PID: pid}, nil
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
