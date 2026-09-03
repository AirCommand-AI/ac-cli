package credentials

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

func TestSaveUsesPerAgentPathSecureModesAndAgentKeyedShape(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	store := NewStore(home)
	want := testCredential("agent-123", "694")
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	for _, directory := range []string{
		filepath.Join(home, ".aircommand"),
		filepath.Join(home, ".aircommand", "agents"),
		filepath.Join(home, ".aircommand", "agents", "agent-123"),
	} {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatalf("stat directory %q: %v", directory, err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Errorf("directory %q mode = %o, want 700", directory, got)
		}
	}

	wantPath := filepath.Join(home, ".aircommand", "agents", "agent-123", "credentials.json")
	if got := store.Path(want.AgentID); got != wantPath {
		t.Fatalf("credential path = %q, want %q", got, wantPath)
	}
	fileInfo, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("stat credential file: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential file mode = %o, want 600", got)
	}

	contents, err := os.ReadFile(wantPath)
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
	if _, err := os.Stat(filepath.Join(home, ".aircommand", "credentials.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old shared credential path exists or could not be checked: %v", err)
	}
}

func TestFindSupportsExplicitAgentAndReportsAmbiguityAcrossAgentFiles(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	first := testCredential("agent-z", "694")
	second := testCredential("agent-a", "694")
	for _, credential := range []Credential{first, second} {
		if err := store.Save(credential); err != nil {
			t.Fatalf("Save(%s): %v", credential.AgentID, err)
		}
	}

	got, err := store.FindByAgent("694", second.AgentID)
	if err != nil {
		t.Fatalf("FindByAgent: %v", err)
	}
	if !reflect.DeepEqual(got, second) {
		t.Fatalf("FindByAgent = %#v, want %#v", got, second)
	}

	// A multi-agent fallback must use directory names only. Invalidating both
	// credential documents proves the lookup does not open either file.
	for _, credential := range []Credential{first, second} {
		if err := os.WriteFile(store.Path(credential.AgentID), []byte("must not be read"), 0o600); err != nil {
			t.Fatalf("invalidate %s credential: %v", credential.AgentID, err)
		}
	}
	_, err = store.FindByWorkstream("694")
	var multiple *MultipleAgentsError
	if !errors.As(err, &multiple) {
		t.Fatalf("FindByWorkstream error = %v, want *MultipleAgentsError", err)
	}
	if want := []string{"agent-a", "agent-z"}; !reflect.DeepEqual(multiple.AgentIDs, want) {
		t.Fatalf("available agent IDs = %v, want %v", multiple.AgentIDs, want)
	}
}

func TestSaveKeepsAgentsInSeparateCredentialFiles(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	first := testCredential("one", "100")
	second := testCredential("two", "200")
	if err := store.Save(first); err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	if err := store.Save(second); err != nil {
		t.Fatalf("Save(second): %v", err)
	}

	if store.Path(first.AgentID) == store.Path(second.AgentID) {
		t.Fatal("two agents share a credential path")
	}
	for _, want := range []Credential{first, second} {
		got, err := store.FindByAgent(want.WorkstreamCode, want.AgentID)
		if err != nil {
			t.Fatalf("FindByAgent(%q): %v", want.AgentID, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("FindByAgent(%q) = %#v, want %#v", want.AgentID, got, want)
		}
	}
}

func TestLegacyLayoutIsRejectedWithoutReadingOrWritingIt(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	oldPath := filepath.Join(storagepath.Root(home), "credentials.json")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatalf("create old directory: %v", err)
	}
	oldContents := []byte("old credentials must not be parsed or replaced")
	if err := os.WriteFile(oldPath, oldContents, 0o600); err != nil {
		t.Fatalf("write old credentials: %v", err)
	}

	store := NewStore(home)
	for operation, err := range map[string]error{
		"Save":             store.Save(testCredential("new-agent", "694")),
		"FindByAgent":      findByAgentError(store, "694", "new-agent"),
		"FindByWorkstream": findByWorkstreamError(store, "694"),
	} {
		var legacy *storagepath.LegacyLayoutError
		if !errors.As(err, &legacy) {
			t.Errorf("%s error = %v, want *LegacyLayoutError", operation, err)
		}
	}
	got, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatalf("read old credentials after rejection: %v", err)
	}
	if !reflect.DeepEqual(got, oldContents) {
		t.Fatalf("old credentials changed to %q", got)
	}
	if _, err := os.Stat(store.Path("new-agent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new credential was written despite old layout: %v", err)
	}
}

func findByAgentError(store *Store, workstreamCode string, agentID string) error {
	_, err := store.FindByAgent(workstreamCode, agentID)
	return err
}

func findByWorkstreamError(store *Store, workstreamCode string) error {
	_, err := store.FindByWorkstream(workstreamCode)
	return err
}

func testCredential(agentID string, workstreamCode string) Credential {
	return Credential{
		APIToken:       "api_" + agentID,
		SocketKey:      "sock_" + agentID,
		WorkstreamCode: workstreamCode,
		AgentID:        agentID,
		SocketAddress:  "wss://socket.aircommand.ai/agent/" + agentID,
	}
}
