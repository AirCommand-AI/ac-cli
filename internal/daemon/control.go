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
	StopAgents   bool     `json:"stopAgents,omitempty"`
}
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type Response struct {
	OK    bool      `json:"ok"`
	Data  any       `json:"data,omitempty"`
	Error *APIError `json:"error,omitempty"`
}

func dispatch(ctx context.Context, supervisor Supervisor, request Request, started time.Time, logPath string, pid int) Response {
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
		case strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "unsupported"):
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
		return Response{OK: true, Data: map[string]any{"version": 1, "startedAt": started.UTC().Format(time.RFC3339Nano), "logPath": logPath, "pid": pid, "agents": agents}}
	case "agent.start":
		if request.Name == "" || request.AgentID == "" || request.Organization == "" || request.Workstream == "" || request.WorkFolder == "" {
			return fail(ErrInvalid)
		}
		if err := supervisor.Start(ctx, Definition{Version: 1, Name: request.Name, AgentID: request.AgentID, Organization: request.Organization, Workstream: request.Workstream, Repos: request.Repos, WorkFolder: request.WorkFolder}); err != nil {
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

// Serve holds the pid lock until all connections close. The socket and pid
// file are owner-only; the lock, not the file's existence, fences stale pids.
func Serve(ctx context.Context, home string, supervisor Supervisor) error {
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
	socket := storagepath.DaemonSocket(home)
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
	running, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var stoppedAgents atomic.Bool
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := supervisor.Run(running); err != nil && running.Err() == nil {
			_, _ = fmt.Fprintln(logFile, err)
			cancel()
		}
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
			serveConnection(running, conn, supervisor, started, storagepath.DaemonLog(home), os.Getpid(), cancel, &stoppedAgents)
		}()
	}
	wg.Wait()
	if !stoppedAgents.Load() {
		return supervisor.Shutdown(context.Background(), false)
	}
	return nil
}
func serveConnection(ctx context.Context, conn net.Conn, supervisor Supervisor, started time.Time, logPath string, pid int, cancel context.CancelFunc, stoppedAgents *atomic.Bool) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	var req Request
	encoder := json.NewEncoder(conn)
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil || len(line) > 1024*1024 || json.Unmarshal(line, &req) != nil {
		_ = encoder.Encode(Response{Error: &APIError{Code: "invalid", Message: "invalid JSON request"}})
		return
	}
	response := dispatch(ctx, supervisor, req, started, logPath, pid)
	_ = encoder.Encode(response)
	if req.Op == "shutdown" && response.OK {
		if req.StopAgents {
			stoppedAgents.Store(true)
		}
		cancel()
	}
}

func Call(ctx context.Context, home string, req Request) (json.RawMessage, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", filepath.Clean(storagepath.DaemonSocket(home)))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
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
