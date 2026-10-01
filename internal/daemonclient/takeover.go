package daemonclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os/exec"
)

type takeoverSpec struct {
	PiPath, WorkDir, SessionID string
	Args                       []string
}

// Takeover keeps the control connection open for the entire foreground pi
// session, so the daemon will not relaunch headless pi on the same session.
func (c Client) Takeover(ctx context.Context, name string, input io.Reader, output, stderr io.Writer) error {
	dial := c.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, "unix", c.SocketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(map[string]any{"op": "agent.takeover", "name": name}); err != nil {
		return err
	}
	var reply struct {
		OK    bool         `json:"ok"`
		Data  takeoverSpec `json:"data"`
		Error *RemoteError `json:"error"`
	}
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		return err
	}
	if !reply.OK {
		if reply.Error != nil {
			return reply.Error
		}
		return errors.New("takeover rejected")
	}
	spec := reply.Data
	command := exec.CommandContext(ctx, spec.PiPath, spec.Args...)
	command.Dir = spec.WorkDir
	command.Stdin = input
	command.Stdout = output
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return err
	}
	if err := json.NewEncoder(conn).Encode(map[string]any{"type": "pid", "pid": command.Process.Pid}); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return err
	}
	// Loss of the daemon's fence must not leave foreground pi writing a
	// session that a restarted daemon may reopen headless.
	finished := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		select {
		case <-finished:
		default:
			_ = command.Process.Kill()
		}
	}()
	err = command.Wait()
	close(finished)
	return err
}
