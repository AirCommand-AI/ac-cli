package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

// Use the supervisor contract directly: the control server and process
// manager share types without copying state or diverging on the wire format.
type Definition = supervisor.AgentDefinition
type Status = supervisor.AgentStatus
type Supervisor = supervisor.Supervisor
type Request struct {
	Op           string   `json:"op"`
	Name         string   `json:"name,omitempty"`
	AgentID      string   `json:"agentId,omitempty"`
	Organization string   `json:"organization,omitempty"`
	Workstream   string   `json:"workstream,omitempty"`
	Repos        []string `json:"repos,omitempty"`
	WorkFolder   string   `json:"workFolder,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	StopAgents   bool     `json:"stopAgents,omitempty"`
	Text         string   `json:"text,omitempty"`
}
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// AircomVersion is the release this daemon binary was built as; the CLI sets it
// at startup so status can tell a running daemon from a newer installed one.
var AircomVersion = "dev"

type Response struct {
	OK    bool      `json:"ok"`
	Data  any       `json:"data,omitempty"`
	Error *APIError `json:"error,omitempty"`
}

func dispatch(ctx context.Context, supervisor Supervisor, request Request, started time.Time, version, logPath string, pid int, socket *SocketClient) Response {
	fail := func(err error) Response {
		code := "internal"
		switch {
		case errors.Is(err, os.ErrNotExist):
			code = "not_found"
		case errors.Is(err, ErrLocked):
			code = "locked"
		case errors.Is(err, ErrAlreadyRunning):
			code = "already_running"
		case errors.Is(err, ErrInvalid):
			code = "invalid"
		// The supervisor currently reports its validation and collision errors
		// as plain errors. Translate these into the stable C3 wire codes.
		case strings.Contains(err.Error(), "already"):
			code = "already_running"
		case strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "unsupported") || strings.Contains(err.Error(), "must be stopped"):
			code = "invalid"
		}
		return Response{Error: &APIError{Code: code, Message: err.Error()}}
	}
	switch request.Op {
	case "status", "agent.list":
		agents, err := supervisor.List(ctx)
		if err != nil {
			return fail(err)
		}
		if agents == nil {
			agents = []Status{}
		}
		if request.Op == "agent.list" {
			return Response{OK: true, Data: map[string]any{"agents": agents}}
		}
		data := map[string]any{"version": 1, "aircomVersion": version, "startedAt": started.UTC().Format(time.RFC3339Nano), "logPath": logPath, "pid": pid, "agents": agents}
		if socket != nil {
			data["connection"] = socket.State()
		}
		return Response{OK: true, Data: data}
	case "agent.start":
		if request.Name == "" || request.AgentID == "" || request.Organization == "" || request.Workstream == "" || request.WorkFolder == "" {
			return fail(ErrInvalid)
		}
		if err := supervisor.Start(ctx, Definition{Version: 1, Name: request.Name, AgentID: request.AgentID, Organization: request.Organization, Workstream: request.Workstream, Repos: request.Repos, WorkFolder: request.WorkFolder, Mode: request.Mode}); err != nil {
			return fail(err)
		}
	case "agent.mode":
		if request.Name == "" || request.Mode == "" {
			return fail(ErrInvalid)
		}
		if err := supervisor.Mode(ctx, request.Name, request.Mode); err != nil {
			return fail(err)
		}
	case "agent.stop":
		if request.Name == "" {
			return fail(ErrInvalid)
		}
		if err := supervisor.Stop(ctx, request.Name); err != nil {
			return fail(err)
		}
	case "agent.remove":
		if request.Name == "" {
			return fail(ErrInvalid)
		}
		if err := supervisor.Remove(ctx, request.Name); err != nil {
			return fail(err)
		}
	case "shutdown":
		if err := supervisor.Shutdown(ctx, request.StopAgents); err != nil {
			return fail(err)
		}
	default:
		return fail(ErrInvalid)
	}
	return Response{OK: true, Data: map[string]any{}}
}

var (
	ErrLocked         = errors.New("daemon already running")
	ErrInvalid        = errors.New("invalid control request")
	ErrAlreadyRunning = errors.New("agent already running")
)

// socketPath checks the macOS sockaddr_un limit (104 bytes including the
// terminating NUL) before either bind or dial; Linux permits slightly more.
func socketPath(home string) (string, error) {
	path := storagepath.DaemonSocket(home)
	if len(path) > 103 {
		return "", fmt.Errorf("AirCommand daemon socket path is too long (%d bytes; maximum 103): %s", len(path), path)
	}
	return path, nil
}

// Serve holds the pid lock until all connections close. The socket and pid
// file are owner-only; the lock, not the file's existence, fences stale pids.
func Serve(ctx context.Context, home string, supervisor Supervisor, sockets ...*SocketClient) error {
	socket, err := socketPath(home)
	if err != nil {
		return err
	}
	directory := storagepath.DaemonDirectory(home)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return err
	}
	pidFile, err := os.OpenFile(storagepath.DaemonPID(home), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer pidFile.Close()
	if err := syscall.Flock(int(pidFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return ErrLocked
		}
		return err
	}
	defer syscall.Flock(int(pidFile.Fd()), syscall.LOCK_UN)
	if err := pidFile.Truncate(0); err != nil {
		return err
	}
	if _, err := pidFile.Seek(0, 0); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(pidFile, "%d\n", os.Getpid()); err != nil {
		return err
	}
	logFile, err := os.OpenFile(storagepath.DaemonLog(home), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	// Only remove a stale socket while holding the pid lock.
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socket)
	if err := os.Chmod(socket, 0600); err != nil {
		return err
	}
	started := time.Now()
	version := AircomVersion // fixed at startup: status reports the running release
	running, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var ws *SocketClient
	if len(sockets) > 0 {
		ws = sockets[0]
		if ws != nil && ws.Log == nil {
			ws.Log = func(line string) { _, _ = fmt.Fprintln(logFile, line) }
		}
	}
	if ws != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ws.Run(running); err != nil && running.Err() == nil {
				_, _ = fmt.Fprintln(logFile, err)
			}
		}()
	}
	var stoppedAgents atomic.Bool
	runErrors := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := supervisor.Run(running)
		if running.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("daemon supervisor exited unexpectedly")
		}
		_, _ = fmt.Fprintln(logFile, err)
		runErrors <- err
		cancel()
	}()
	go func() { <-running.Done(); _ = listener.Close() }()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if running.Err() != nil {
				break
			}
			cancel()
			wg.Wait()
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			serveConnection(running, conn, supervisor, started, version, storagepath.DaemonLog(home), os.Getpid(), cancel, &stoppedAgents, ws)
		}()
	}
	wg.Wait()
	select {
	case err := <-runErrors:
		return err
	default:
	}
	if !stoppedAgents.Load() {
		return supervisor.Shutdown(context.Background(), false)
	}
	return nil
}
func serveConnection(ctx context.Context, conn net.Conn, supervisor Supervisor, started time.Time, version, logPath string, pid int, cancel context.CancelFunc, stoppedAgents *atomic.Bool, socket *SocketClient) {
	defer conn.Close()
	var req Request
	encoder := json.NewEncoder(conn)
	// One reader for the lifetime of the stream preserves buffered client input.
	reader := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	line, err := reader.ReadBytes('\n')
	if err != nil || len(line) > 1024*1024 || json.Unmarshal(line, &req) != nil {
		_ = encoder.Encode(Response{Error: &APIError{Code: "invalid", Message: "invalid JSON request"}})
		return
	}
	if req.Op == "agent.attach" {
		_ = conn.SetDeadline(time.Time{})
		serveAttach(ctx, conn, reader, supervisor, req)
		return
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	response := dispatch(ctx, supervisor, req, started, version, logPath, pid, socket)
	_ = encoder.Encode(response)
	if req.Op == "shutdown" && response.OK {
		if req.StopAgents {
			stoppedAgents.Store(true)
		}
		cancel()
	}
}

// serveAttach owns one viewer only; the supervisor's event consumer is never
// blocked by the viewer. The control socket's 0600 permissions restrict access.
func serveAttach(ctx context.Context, conn net.Conn, reader *bufio.Reader, m Supervisor, req Request) {
	encoder := json.NewEncoder(conn)
	events, cancel, ok := m.Subscribe(req.Name)
	if !ok {
		_ = encoder.Encode(Response{Error: &APIError{Code: "not_found", Message: "headless agent not running"}})
		return
	}
	defer cancel()
	_ = encoder.Encode(Response{OK: true, Data: map[string]any{"mode": "headless"}})
	incoming := make(chan Request, 1)
	go func() {
		defer close(incoming)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var item Request
			if json.Unmarshal(line, &item) != nil {
				return
			}
			select {
			case incoming <- item:
			case <-ctx.Done():
				return
			}
		}
	}()
	// Shutdown must close the socket to unblock writes to an unresponsive viewer.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, open := <-events:
			if !open {
				return
			}
			if err := encoder.Encode(map[string]any{"type": "event", "event": ev}); err != nil {
				return
			}
		case item, open := <-incoming:
			if !open || item.Op == "detach" {
				return
			}
			if item.Op == "interrupt" {
				_ = encoder.Encode(map[string]any{"type": "banner", "text": "Interrupt is not available until the audited machine route is connected"})
				continue
			}
			if item.Op == "say" && strings.TrimSpace(item.Text) != "" {
				driver, ok := m.Driver(req.Name)
				if !ok {
					return
				}
				if err := driver.Send(pidriver.Outgoing{Text: item.Text, Kind: pidriver.Urgent, Source: "attach"}); err != nil {
					_ = encoder.Encode(map[string]any{"type": "banner", "text": err.Error()})
					return
				}
			}
		}
	}
}

func Call(ctx context.Context, home string, req Request) (json.RawMessage, error) {
	var dialer net.Dialer
	socket, err := socketPath(home)
	if err != nil {
		return nil, err
	}
	conn, err := dialer.DialContext(ctx, "unix", filepath.Clean(socket))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(30 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	var response struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error *APIError       `json:"error"`
	}
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return nil, err
	}
	if !response.OK {
		if response.Error == nil {
			return nil, errors.New("daemon rejected request")
		}
		return nil, fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
	}
	return response.Data, nil
}
