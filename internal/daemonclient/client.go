// Package daemonclient implements the daemon's newline-delimited local control protocol (C3).
package daemonclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Client connects once per request; the daemon sends exactly one response.
type Client struct {
	SocketPath string
	Dial       func(context.Context, string, string) (net.Conn, error)
}

type AgentStatus struct {
	Name       string          `json:"name"`
	AgentID    string          `json:"agentId"`
	Workstream string          `json:"workstream"`
	Desired    string          `json:"desired"`
	State      string          `json:"state"`
	Mode       string          `json:"mode"`
	PiState    string          `json:"piState"`
	PID        int             `json:"pid"`
	LastExit   json.RawMessage `json:"lastExit"`
	LastPollAt string          `json:"lastPollAt"`
}

type Status struct {
	Version   int           `json:"version"`
	StartedAt string        `json:"startedAt"`
	LogPath   string        `json:"logPath"`
	PID       int           `json:"pid"`
	Agents    []AgentStatus `json:"agents"`
}

type StartRequest struct {
	Name         string   `json:"name"`
	AgentID      string   `json:"agentId"`
	Organization string   `json:"organization"`
	Workstream   string   `json:"workstream"`
	Repos        []string `json:"repos"`
	WorkFolder   string   `json:"workFolder"`
	Mode         string   `json:"mode,omitempty"`
}

type RemoteError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RemoteError) Error() string { return fmt.Sprintf("daemon %s: %s", e.Code, e.Message) }

func (c Client) call(ctx context.Context, request any, result any) error {
	if c.SocketPath == "" {
		return errors.New("daemon socket path is empty")
	}
	dial := c.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, "unix", c.SocketPath)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w", err)
	}
	defer conn.Close()
	// A stalled local daemon cannot hold a command indefinitely.
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return fmt.Errorf("send daemon request: %w", err)
	}
	var response struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error *RemoteError    `json:"error"`
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read daemon response: %w", err)
	}
	if err := json.Unmarshal(line, &response); err != nil {
		return fmt.Errorf("decode daemon response: %w", err)
	}
	if !response.OK {
		if response.Error == nil || strings.TrimSpace(response.Error.Code) == "" {
			return errors.New("daemon returned an invalid error response")
		}
		return response.Error
	}
	if result != nil && len(response.Data) != 0 && string(response.Data) != "null" {
		if err := json.Unmarshal(response.Data, result); err != nil {
			return fmt.Errorf("decode daemon data: %w", err)
		}
	}
	return nil
}

func (c Client) Status(ctx context.Context) (Status, error) {
	var status Status
	err := c.call(ctx, map[string]any{"op": "status"}, &status)
	return status, err
}

func (c Client) List(ctx context.Context) ([]AgentStatus, error) {
	var result struct {
		Agents []AgentStatus `json:"agents"`
	}
	err := c.call(ctx, map[string]any{"op": "agent.list"}, &result)
	return result.Agents, err
}

func (c Client) Start(ctx context.Context, r StartRequest) error {
	return c.call(ctx, struct {
		Op string `json:"op"`
		StartRequest
	}{"agent.start", r}, nil)
}

func (c Client) Mode(ctx context.Context, name, mode string) error {
	return c.call(ctx, map[string]any{"op": "agent.mode", "name": name, "mode": mode}, nil)
}

func (c Client) Stop(ctx context.Context, name string) error {
	return c.call(ctx, map[string]any{"op": "agent.stop", "name": name}, nil)
}

func (c Client) Remove(ctx context.Context, name string) error {
	return c.call(ctx, map[string]any{"op": "agent.remove", "name": name}, nil)
}

func (c Client) Shutdown(ctx context.Context, stopAgents bool) error {
	return c.call(ctx, map[string]any{"op": "shutdown", "stopAgents": stopAgents}, nil)
}
