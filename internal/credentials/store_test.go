package credentials

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSaveUsesSecureModesAndAgentKeyedShape(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	store := NewStore(home)
	want := Credential{
		APIToken:       "api_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SocketKey:      "sock_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		WorkstreamCode: "694",
		AgentID:        "agent-123",
		SocketAddress:  "wss://socket.aircommand.ai/agent",
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	directoryInfo, err := os.Stat(filepath.Join(home, ".aircommand"))
	if err != nil {
		t.Fatalf("stat credential directory: %v", err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("credential directory mode = %o, want 700", got)
	}

	fileInfo, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat credential file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential file mode = %o, want 600", got)
	}

	contents, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read credential file: %v", err)
	}
	var file File
	if err := json.Unmarshal(contents, &file); err != nil {
		t.Fatalf("decode credential file: %v", err)
	}
	if file.Version != 1 {
		t.Fatalf("credential file version = %d, want 1", file.Version)
	}
	if len(file.Agents) != 1 {
		t.Fatalf("credential file has %d agents, want 1", len(file.Agents))
	}
	if got := file.Agents[want.AgentID]; !reflect.DeepEqual(got, want) {
		t.Fatalf("stored credential = %#v, want %#v", got, want)
	}
}

func TestSavePreservesOtherAgents(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	first := Credential{APIToken: "api_one", SocketKey: "sock_one", WorkstreamCode: "100", AgentID: "one", SocketAddress: "wss://one"}
	second := Credential{APIToken: "api_two", SocketKey: "sock_two", WorkstreamCode: "200", AgentID: "two", SocketAddress: "wss://two"}
	if err := store.Save(first); err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	if err := store.Save(second); err != nil {
		t.Fatalf("Save(second): %v", err)
	}

	for _, want := range []Credential{first, second} {
		got, err := store.FindByWorkstream(want.WorkstreamCode)
		if err != nil {
			t.Fatalf("FindByWorkstream(%q): %v", want.WorkstreamCode, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("FindByWorkstream(%q) = %#v, want %#v", want.WorkstreamCode, got, want)
		}
	}
}
