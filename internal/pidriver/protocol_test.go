package pidriver

import (
	"bytes"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
)

var testProtocolDriver *RPC

func protocolDriver() (*RPC, *bytes.Buffer) {
	d := New(Options{}).(*RPC)
	testProtocolDriver = d
	b := new(bytes.Buffer)
	d.stdin = writeNopCloser{b}
	d.state.Ready = true
	close(d.ready)
	return d, b
}

type writeNopCloser struct{ *bytes.Buffer }

func (writeNopCloser) Close() error { return nil }
func record(t *testing.T, b *bytes.Buffer) map[string]any {
	t.Helper()
	testProtocolDriver.mu.Lock()
	for _, data := range testProtocolDriver.writes {
		_, _ = b.Write(data)
	}
	testProtocolDriver.writes = nil
	testProtocolDriver.mu.Unlock()
	line, err := b.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func TestPromptBehaviorAndPrefix(t *testing.T) {
	for _, tc := range []struct {
		kind     Kind
		behavior string
	}{{Regular, "followUp"}, {Urgent, "steer"}} {
		d, b := protocolDriver()
		d.dispatch(Outgoing{Kind: tc.kind, Text: "/danger"})
		v := record(t, b)
		if v["type"] != "prompt" || v["streamingBehavior"] != tc.behavior || v["message"] != "[AirCommand] /danger" {
			t.Fatalf("%+v: %v", tc, v)
		}
	}
}
func TestCompactionRetry(t *testing.T) {
	d, b := protocolDriver()
	d.dispatch(Outgoing{Kind: Regular, Text: "hello"})
	original := record(t, b)
	response, _ := json.Marshal(map[string]any{"type": "response", "id": original["id"], "command": "prompt", "success": false, "error": "Cannot prompt during compaction"})
	d.handle(response)
	if !d.compacting || len(d.outgoing) != 1 {
		t.Fatalf("not queued for retry: %+v", d)
	}
	d.handle([]byte(`{"type":"compaction_end"}`))
	if d.compacting {
		t.Fatal("compaction did not end")
	}
	d.dispatch(d.outgoing[0])
	retried := record(t, b)
	if retried["message"] != original["message"] {
		t.Fatal("retry lost prompt")
	}
}
func TestDialogCancelAndToolEvent(t *testing.T) {
	d, b := protocolDriver()
	d.handle([]byte(`{"type":"extension_ui_request","id":"dialog-1","method":"editor","title":"Edit"}`))
	v := record(t, b)
	if v["type"] != "extension_ui_response" || v["id"] != "dialog-1" || v["cancelled"] != true {
		t.Fatalf("dialog not cancelled: %v", v)
	}
	if ev := <-d.Events(); ev.Kind != "dialog_cancelled" {
		t.Fatal(ev)
	}
	d.handle([]byte(`{"type":"tool_execution_start","toolCallId":"nested","parentToolCallId":"parent","toolName":"bash"}`))
	if snap := d.State(); snap.CurrentTool != "bash" || snap.ParentToolCallID != "parent" {
		t.Fatalf("nested tool state: %+v", snap)
	}
	d.handle([]byte(`{"type":"tool_execution_start","toolCallId":"child","parentToolCallId":"nested","toolName":"read"}`))
	d.handle([]byte(`{"type":"tool_execution_end","toolCallId":"child"}`))
	if d.State().CurrentTool != "bash" {
		t.Fatal("parent tool lost when child ended")
	}
	d.handle([]byte(`{"type":"agent_settled"}`))
	if !d.State().Settled {
		t.Fatal("not settled")
	}
}
func TestInterruptClearsBeforeAbortAndRestores(t *testing.T) {
	d, b := protocolDriver()
	d.dispatch(Outgoing{Kind: Interrupt, Text: "/now"})
	clear := record(t, b)
	if clear["type"] != "clear_queue" {
		t.Fatal(clear)
	}
	d.handle([]byte(`{"type":"response","id":"1","command":"clear_queue","success":true,"data":{"steering":["first"],"followUp":["second"]}}`))
	abort := record(t, b)
	if abort["type"] != "abort" {
		t.Fatal(abort)
	}
	d.handle([]byte(`{"type":"response","id":"2","command":"abort","success":true}`))
	prompt := record(t, b)
	if prompt["type"] != "prompt" || prompt["message"] != "[AirCommand] /now" {
		t.Fatal(prompt)
	}
	d.outgoing = append(d.outgoing, Outgoing{Text: "later"})
	d.handle([]byte(`{"type":"response","id":"3","command":"prompt","success":true}`))
	if len(d.outgoing) != 3 || d.outgoing[0].Text != "first" || d.outgoing[1].Text != "second" {
		t.Fatalf("lost queue: %+v", d.outgoing)
	}
}
func TestProcSnapshot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux procfs")
	}
	if got := processStartTime(os.Getpid()); got == "" {
		t.Fatal("missing /proc starttime")
	}
	if got := processCmdline(os.Getpid()); len(got) == 0 {
		t.Fatal("missing /proc cmdline")
	}
}
func TestGuidance(t *testing.T) {
	s := FormatMessageGuidance("/bin/air'com", "980", "agent", "wake", "mid", "sender")
	if !strings.Contains(s, `'/bin/air'"'"'com' inbox`) || !strings.Contains(s, "Pointer only, no body.") || !strings.Contains(s, "ack") {
		t.Fatal(s)
	}
}
