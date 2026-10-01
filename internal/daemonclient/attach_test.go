package daemonclient

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderDeliveredOnUserStart(t *testing.T) {
	out := new(bytes.Buffer)
	renderEvent(out, "message_start", map[string]any{"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "[AirCommand] wake\nDelivery kind: urgent"}}}})
	if !strings.Contains(out.String(), "urgent delivered") || !strings.Contains(out.String(), "user: [AirCommand] wake") {
		t.Fatal(out.String())
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
