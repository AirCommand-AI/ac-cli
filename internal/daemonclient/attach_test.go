package daemonclient

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

func TestAttachRejectsEmptyInterrupt(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	c := Client{SocketPath: "unused", Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	frames := make(chan map[string]any, 2)
	go func() {
		defer server.Close()
		dec := json.NewDecoder(server)
		enc := json.NewEncoder(server)
		var req map[string]any
		_ = dec.Decode(&req)
		_ = enc.Encode(map[string]any{"ok": true})
		for {
			var item map[string]any
			if dec.Decode(&item) != nil {
				return
			}
			frames <- item
			if item["op"] == "detach" {
				return
			}
		}
	}()
	called := false
	output := new(bytes.Buffer)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Attach(ctx, "agent", strings.NewReader("/interrupt\n/detach\n"), output, func(string) error { called = true; return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if called || !strings.Contains(output.String(), "requires a message") {
		t.Fatalf("empty interrupt accepted: %v %s", called, output)
	}
	if item := <-frames; item["op"] != "detach" {
		t.Fatal(item)
	}
}
func TestRenderDeliveredOnUserStart(t *testing.T) {
	out := new(bytes.Buffer)
	renderEvent(out, "message_start", map[string]any{"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "[AirCommand] wake\nDelivery kind: urgent"}}}})
	if !strings.Contains(out.String(), "urgent delivered") || !strings.Contains(out.String(), "user: [AirCommand] wake") {
		t.Fatal(out.String())
	}
}
func TestRenderDeliveryKindBanners(t *testing.T) {
	for _, kind := range []string{"urgent", "nudge", "interrupt"} {
		out := new(bytes.Buffer)
		renderMessage(out, map[string]any{"role": "user", "content": "[AirCommand] wake\nDelivery kind: " + kind}, true)
		if !strings.Contains(out.String(), kind+" delivered") {
			t.Fatalf("kind %s: %s", kind, out)
		}
	}
}
func TestAttachSkipsToolOnlyAssistantRows(t *testing.T) {
	out := new(bytes.Buffer)
	renderMessage(out, map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "toolCall", "name": "bash"}}}, false)
	text := false
	renderEventWithState(out, "message_start", map[string]any{"message": map[string]any{"role": "assistant"}}, &text)
	renderEventWithState(out, "tool_execution_start", map[string]any{"toolName": "bash"}, &text)
	renderEventWithState(out, "message_end", map[string]any{"message": map[string]any{"role": "assistant"}}, &text)
	if got := out.String(); got != "[tool] bash\n" {
		t.Fatalf("tool-only output: %q", got)
	}
	renderEventWithState(out, "message_start", map[string]any{"message": map[string]any{"role": "assistant"}}, &text)
	renderEventWithState(out, "message_update", map[string]any{"assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "done"}}, &text)
	renderEventWithState(out, "message_end", map[string]any{"message": map[string]any{"role": "assistant"}}, &text)
	if got := out.String(); got != "[tool] bash\ndone\n" {
		t.Fatalf("assistant text output: %q", got)
	}
}
func TestRenderToolAndAssistantDelta(t *testing.T) {
	out := new(bytes.Buffer)
	renderEvent(out, "tool_execution_start", map[string]any{"toolName": "bash"})
	renderEvent(out, "message_update", map[string]any{"assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "hello"}})
	if got := out.String(); got != "[tool] bash\nhello" {
		t.Fatal(got)
	}
}
