package daemonclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
)

// Attach holds an unbounded-deadline control connection for one headless
// viewer. A detached viewer does not stop the agent.
func (c Client) Attach(ctx context.Context, name string, input io.Reader, output io.Writer, interrupt func(string) error) error {
	dial := c.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, "unix", c.SocketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	enc := json.NewEncoder(conn)
	if err := enc.Encode(map[string]any{"op": "agent.attach", "name": name}); err != nil {
		return err
	}
	decoder := json.NewDecoder(conn)
	var initial struct {
		OK    bool         `json:"ok"`
		Error *RemoteError `json:"error"`
	}
	if err := decoder.Decode(&initial); err != nil {
		return err
	}
	if !initial.OK {
		if initial.Error != nil {
			return initial.Error
		}
		return fmt.Errorf("attach rejected")
	}
	go func() { <-ctx.Done(); _ = conn.Close() }()
	go func() {
		scanner := bufio.NewScanner(input)
		for scanner.Scan() {
			text := scanner.Text()
			op := "say"
			if text == "/detach" {
				op = "detach"
			} else if strings.HasPrefix(text, "/interrupt ") {
				text = strings.TrimPrefix(text, "/interrupt ")
				if interrupt != nil {
					if err := interrupt(text); err != nil {
						fmt.Fprintf(output, "[AirCommand] Interrupt failed: %v\n", err)
					}
				}
				continue
			}
			if enc.Encode(map[string]any{"op": op, "text": text}) != nil || op == "detach" {
				return
			}
		}
		_ = enc.Encode(map[string]any{"op": "detach"})
	}()
	for {
		var item struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Entry json.RawMessage `json:"entry"`
			Event struct {
				Kind string         `json:"Kind"`
				Data map[string]any `json:"Data"`
			} `json:"event"`
		}
		if err := decoder.Decode(&item); err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return nil
			}
			return err
		}
		switch item.Type {
		case "history":
			fmt.Fprintf(output, "[history] %s\n", eventSummary(map[string]any{"text": string(item.Entry)}))
		case "event":
			fmt.Fprintf(output, "[%s] %s\n", item.Event.Kind, eventSummary(item.Event.Data))
		case "banner":
			fmt.Fprintf(output, "[AirCommand] %s\n", item.Text)
		case "state":
			fmt.Fprintf(output, "[state] %s\n", item.Text)
		}
	}
}
func eventSummary(data map[string]any) string {
	for _, key := range []string{"toolName", "message", "text"} {
		if s, ok := data[key].(string); ok {
			s = strings.ReplaceAll(s, "\n", " ")
			if len(s) > 180 {
				s = s[:180] + "…"
			}
			return s
		}
	}
	return ""
}
