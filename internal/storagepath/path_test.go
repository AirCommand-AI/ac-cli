package storagepath

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFilenameComponentKeepsOrdinaryIDsAndContainsCraftedIDs(t *testing.T) {
	t.Parallel()

	if got, want := FilenameComponent("agm_123-ABC.xyz"), "agm_123-ABC.xyz"; got != want {
		t.Fatalf("ordinary component = %q, want %q", got, want)
	}
	if got, ok := ValueFromFilenameComponent("agm_123-ABC.xyz"); !ok || got != "agm_123-ABC.xyz" {
		t.Fatalf("ordinary component round trip = %q, %v", got, ok)
	}

	crafted := []string{".", "..", "../other-agent", "agent/../../other", "agent\\other", "id-Li4"}
	seen := make(map[string]string)
	for _, agentID := range crafted {
		component := FilenameComponent(agentID)
		if component == "." || component == ".." || strings.ContainsAny(component, `/\\`) {
			t.Errorf("FilenameComponent(%q) = unsafe component %q", agentID, component)
		}
		if previous, exists := seen[component]; exists {
			t.Errorf("FilenameComponent collision: %q and %q both map to %q", previous, agentID, component)
		}
		seen[component] = agentID
		if got, ok := ValueFromFilenameComponent(component); !ok || got != agentID {
			t.Errorf("component %q round trip = %q, %v; want %q, true", component, got, ok, agentID)
		}

		home := t.TempDir()
		directory := AgentDirectory(home, agentID)
		relative, err := filepath.Rel(AgentsDirectory(home), directory)
		if err != nil {
			t.Fatalf("Rel(%q): %v", agentID, err)
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			t.Errorf("agent %q escaped agents directory: %q", agentID, directory)
		}
	}

	if FilenameComponent("..") == FilenameComponent("id-Li4") {
		t.Fatal("encoded '..' collides with an ordinary ID resembling its encoded form")
	}
}

func TestCheckLegacyLayoutReportsEveryOldLocationWithoutReadingIt(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	root := Root(home)
	if err := os.MkdirAll(filepath.Join(root, "state"), 0o700); err != nil {
		t.Fatalf("create old state directory: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "spool"), 0o700); err != nil {
		t.Fatalf("create old spool directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "credentials.json"), []byte("not valid JSON and must not be read"), 0o600); err != nil {
		t.Fatalf("create old credentials: %v", err)
	}

	err := CheckLegacyLayout(home)
	var legacy *LegacyLayoutError
	if !errors.As(err, &legacy) {
		t.Fatalf("CheckLegacyLayout error = %v, want *LegacyLayoutError", err)
	}
	want := []string{
		filepath.Join(root, "credentials.json"),
		filepath.Join(root, "state"),
		filepath.Join(root, "spool"),
	}
	if !reflect.DeepEqual(legacy.Paths, want) {
		t.Fatalf("legacy paths = %v, want %v", legacy.Paths, want)
	}
}
