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
func (c Client) Attach(ctx context.Context, name string, input io.Reader, output io.Writer, interrupt func(string) error, posted func(string) error) error {
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
			if op == "say" && posted != nil {
				if err := posted(text); err != nil {
					fmt.Fprintf(output, "[AirCommand] Say sent but update failed: %v\n", err)
				}
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
			var entry struct {
				Message map[string]any `json:"message"`
			}
			if json.Unmarshal(item.Entry, &entry) == nil && entry.Message != nil {
				renderMessage(output, entry.Message, false)
			}
		case "event":
			renderEvent(output, item.Event.Kind, item.Event.Data)
		case "banner":
			fmt.Fprintf(output, "[AirCommand] %s\n", item.Text)
		case "state":
			fmt.Fprintf(output, "[state] %s\n", item.Text)
		}
	}
}
func renderEvent(out io.Writer, kind string, data map[string]any) {
	switch kind {
	case "message_start":
		if msg, ok := data["message"].(map[string]any); ok && msg["role"] == "user" {
			renderMessage(out, msg, true)
		}
	case "message_update":
		if part, ok := data["assistantMessageEvent"].(map[string]any); ok && part["type"] == "text_delta" {
			fmt.Fprint(out, part["delta"])
		}
	case "message_end":
		if msg, ok := data["message"].(map[string]any); ok && msg["role"] == "assistant" {
			fmt.Fprintln(out)
		}
	case "tool_execution_start":
		fmt.Fprintf(out, "[tool] %s\n", eventSummary(data))
	case "auto_retry_start", "compaction_start", "agent_start", "agent_settled", "dialog_cancelled", "rpc_error":
		fmt.Fprintf(out, "[%s] %s\n", kind, eventSummary(data))
	}
}
func renderMessage(out io.Writer, msg map[string]any, live bool) {
	role, _ := msg["role"].(string)
	if role != "assistant" && role != "user" {
		return
	}
	var parts []string
	if content, ok := msg["content"].(string); ok {
		parts = append(parts, content)
	}
	if blocks, ok := msg["content"].([]any); ok {
		for _, v := range blocks {
			if b, ok := v.(map[string]any); ok {
				if text, ok := b["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
	}
	text := strings.Join(parts, "")
	if live && role == "user" && strings.HasPrefix(text, "[AirCommand] ") {
		fmt.Fprintln(out, "[AirCommand] delivered (user message_start)")
	}
	if !live || role == "user" {
		fmt.Fprintf(out, "%s: %s\n", role, text)
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
