package claudedriver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/pidriver"
)

type streamEvent struct {
	Type         string          `json:"type"`
	Subtype      string          `json:"subtype"`
	APIKeySource string          `json:"apiKeySource"`
	Model        string          `json:"model"`
	Version      string          `json:"claude_code_version"`
	Message      json.RawMessage `json:"message"`
	RequestID    string          `json:"request_id"`
	Response     json.RawMessage `json:"response"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func (m message) blocks() []contentBlock {
	var blocks []contentBlock
	if json.Unmarshal(m.Content, &blocks) == nil {
		return blocks
	}
	var text string
	if json.Unmarshal(m.Content, &text) == nil && text != "" {
		return []contentBlock{{Type: "text", Text: text}}
	}
	return nil
}

// handle translates one stream-json line into the pi event kinds consumers
// understand: agent_start, agent_settled, message_start/update/end,
// tool_execution_start/end. Unknown lines become rpc events.
func (d *Driver) handle(line []byte) {
	var ev streamEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}
	var raw map[string]any
	_ = json.Unmarshal(line, &raw)
	now := d.now()
	switch {
	case ev.Type == "system" && ev.Subtype == "init":
		if ev.APIKeySource != "none" {
			d.refuse()
			return
		}
		d.mu.Lock()
		d.state.Streaming, d.state.Settled, d.state.LastEvent = true, false, now
		d.mu.Unlock()
		d.emit(pidriver.Event{Kind: "agent_start", Data: raw, At: now})
	case ev.Type == "result":
		d.mu.Lock()
		d.state.Streaming, d.state.Settled, d.state.CurrentTool, d.state.LastEvent = false, true, "", now
		d.tools = map[string]string{}
		d.mu.Unlock()
		d.emit(pidriver.Event{Kind: "agent_settled", Data: raw, At: now})
	case ev.Type == "assistant":
		d.assistant(ev.Message, now)
	case ev.Type == "user":
		d.userEcho(ev.Message, now)
	case ev.Type == "control_response":
		d.controlResponse(ev.Response)
	default:
		d.mu.Lock()
		d.state.LastEvent = now
		d.mu.Unlock()
		d.emit(pidriver.Event{Kind: "rpc", Data: raw, At: now})
	}
}

func (d *Driver) assistant(raw json.RawMessage, now time.Time) {
	var m message
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	d.emit(pidriver.Event{Kind: "message_start", Data: map[string]any{"message": map[string]any{"role": "assistant"}}, At: now})
	for _, b := range m.blocks() {
		switch b.Type {
		case "text":
			if b.Text != "" {
				d.emit(pidriver.Event{Kind: "message_update", Data: map[string]any{"assistantMessageEvent": map[string]any{"type": "text_delta", "delta": b.Text}}, At: now})
			}
		case "tool_use":
			var args map[string]any
			_ = json.Unmarshal(b.Input, &args)
			d.mu.Lock()
			d.tools[b.ID] = b.Name
			d.state.CurrentTool = b.Name
			d.state.LastEvent = now
			d.mu.Unlock()
			d.emit(pidriver.Event{Kind: "tool_execution_start", Data: map[string]any{"toolName": b.Name, "toolCallId": b.ID, "args": args}, At: now})
		}
	}
	d.mu.Lock()
	d.state.LastEvent = now
	d.mu.Unlock()
	d.emit(pidriver.Event{Kind: "message_end", Data: map[string]any{"message": map[string]any{"role": "assistant"}}, At: now})
}

func (d *Driver) userEcho(raw json.RawMessage, now time.Time) {
	var m message
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	for _, b := range m.blocks() {
		if b.Type != "tool_result" {
			continue
		}
		d.mu.Lock()
		name := d.tools[b.ToolUseID]
		delete(d.tools, b.ToolUseID)
		if len(d.tools) == 0 {
			d.state.CurrentTool = ""
		}
		d.state.LastEvent = now
		d.mu.Unlock()
		d.emit(pidriver.Event{Kind: "tool_execution_end", Data: map[string]any{"toolName": name, "toolCallId": b.ToolUseID}, At: now})
	}
}

func (d *Driver) controlResponse(raw json.RawMessage) {
	var resp struct {
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(raw, &resp) != nil {
		return
	}
	d.mu.Lock()
	matched := resp.RequestID != "" && resp.RequestID == d.interrupting
	msg := d.interruptMsg
	if matched {
		d.interrupting = ""
		d.outgoing = append([]pidriver.Outgoing{msg}, d.outgoing...)
	}
	d.mu.Unlock()
	if matched {
		d.wake()
	}
}

// refuse kills claude after it reported a non-subscription credential.
func (d *Driver) refuse() {
	d.mu.Lock()
	d.refusal = ErrAPIKey
	cmd := d.cmd
	d.mu.Unlock()
	d.emit(pidriver.Event{Kind: "rpc_error", Data: map[string]any{"message": ErrAPIKey.Error()}, At: d.now()})
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// History reads the session transcript: user and assistant entries after
// since (a transcript uuid), at most limit, as {"id","message"} entries.
func (d *Driver) History(ctx context.Context, since string, limit int) ([]json.RawMessage, string, error) {
	d.mu.Lock()
	configDir, session := d.configDir, d.sessionID
	d.mu.Unlock()
	if limit <= 0 {
		return nil, since, nil
	}
	path := transcript(configDir, session)
	if path == "" {
		return nil, since, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	var entries []json.RawMessage
	var ids []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		var line struct {
			Type    string          `json:"type"`
			UUID    string          `json:"uuid"`
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) != nil || (line.Type != "user" && line.Type != "assistant") || line.UUID == "" {
			continue
		}
		entry, err := json.Marshal(map[string]any{"id": line.UUID, "message": line.Message})
		if err != nil {
			continue
		}
		if since != "" && line.UUID == since {
			entries, ids = nil, nil
			continue
		}
		entries, ids = append(entries, entry), append(ids, line.UUID)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return nil, "", err
	}
	if len(entries) > limit {
		entries, ids = entries[len(entries)-limit:], ids[len(ids)-limit:]
	}
	next := since
	if len(ids) > 0 {
		next = ids[len(ids)-1]
	}
	return entries, next, nil
}
