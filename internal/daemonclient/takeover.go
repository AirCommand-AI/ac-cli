package daemonclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type synchronizedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (s *synchronizedWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writer.Write(p)
}

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
	// A distinct process group lets a machine stop terminate pi and its tool
	// children without signalling the operator's shell or this CLI.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	ttyFD := -1
	if f, ok := input.(*os.File); ok {
		if fd, ok := foregroundTTY(f); ok {
			ttyFD = fd
			command.SysProcAttr.Foreground = true
			command.SysProcAttr.Ctty = fd
		}
	}
	command.Stdout = output
	safeStderr := &synchronizedWriter{writer: stderr}
	command.Stderr = safeStderr
	if err := command.Start(); err != nil {
		return err
	}
	if err := json.NewEncoder(conn).Encode(map[string]any{"type": "pid", "pid": command.Process.Pid}); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return err
	}
	// A daemon restart can close the socket while the person is still using
	// pi. The persisted takeover fences the restarted daemon; never kill pi.
	lost := make(chan struct{})
	var stopping atomic.Bool
	waitResult := make(chan error, 1)
	go func() {
		defer close(lost)
		decoder := json.NewDecoder(conn)
		for {
			var frame struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if decoder.Decode(&frame) != nil {
				return
			}
			if frame.Type == "error" {
				fmt.Fprintf(safeStderr, "takeover daemon error: %s\n", frame.Text)
			}
			if frame.Type == "notice" {
				stopping.Store(true)
				fmt.Fprintln(safeStderr, frame.Text)
			}
		}
	}()
	go func() {
		err := command.Wait()
		if ttyFD >= 0 {
			restoreForeground(ttyFD)
		}
		waitResult <- err
	}()
	select {
	case err = <-waitResult:
		// If the peer closed at the same instant pi exited, report that loss
		// before our own Close makes it indistinguishable from normal exit.
		select {
		case <-lost:
			if !stopping.Load() {
				fmt.Fprintln(safeStderr, "daemon connection lost; your pi keeps running; headless resumes after you exit")
			}
		case <-time.After(10 * time.Millisecond):
		}
		_ = conn.Close()
		<-lost
		return err
	case <-lost:
		if !stopping.Load() {
			fmt.Fprintln(safeStderr, "daemon connection lost; your pi keeps running; headless resumes after you exit")
		}
		return <-waitResult
	}
}
