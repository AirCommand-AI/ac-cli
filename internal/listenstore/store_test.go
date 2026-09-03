package listenstore_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

func TestTwoAgentsUseEntirelyDisjointStorageTrees(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	credentialStore := credentials.NewStore(home)
	listenerStore := listenstore.NewStore(home)
	agents := []struct {
		id           string
		workstream   string
		cursor       string
		notification string
	}{
		{id: "agent-claude", workstream: "694", cursor: "cursor-claude", notification: "only claude"},
		{id: "agent-pi", workstream: "694", cursor: "cursor-pi", notification: "only pi"},
	}

	for _, agent := range agents {
		credential := credentials.Credential{
			APIToken:       "api_" + agent.id,
			SocketKey:      "sock_" + agent.id,
			WorkstreamCode: agent.workstream,
			AgentID:        agent.id,
			SocketAddress:  "wss://socket.example/" + agent.id,
		}
		if err := credentialStore.Save(credential); err != nil {
			t.Fatalf("Save credential for %s: %v", agent.id, err)
		}
		if err := listenerStore.SaveCursor(agent.id, agent.cursor); err != nil {
			t.Fatalf("SaveCursor for %s: %v", agent.id, err)
		}
		if err := listenerStore.AppendNotification(agent.id, map[string]string{"summary": agent.notification}); err != nil {
			t.Fatalf("AppendNotification for %s: %v", agent.id, err)
		}
	}

	firstDirectory := storagepath.AgentDirectory(home, agents[0].id)
	secondDirectory := storagepath.AgentDirectory(home, agents[1].id)
	if firstDirectory == secondDirectory {
		t.Fatal("two agent IDs map to the same directory")
	}
	for index, agent := range agents {
		directory := storagepath.AgentDirectory(home, agent.id)
		entries, err := treeFiles(directory)
		if err != nil {
			t.Fatalf("list tree for %s: %v", agent.id, err)
		}
		if want := []string{"credentials.json", "spool.jsonl", "state.json"}; !reflect.DeepEqual(entries, want) {
			t.Errorf("files for %s = %v, want %v", agent.id, entries, want)
		}

		info, err := os.Stat(directory)
		if err != nil {
			t.Fatalf("stat agent directory for %s: %v", agent.id, err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Errorf("agent directory for %s mode = %o, want 700", agent.id, got)
		}
		for _, path := range []string{
			credentialStore.Path(agent.id),
			listenerStore.StatePath(agent.id),
			listenerStore.SpoolPath(agent.id),
		} {
			if filepath.Dir(path) != directory {
				t.Errorf("agent %s owns path outside its directory: %q", agent.id, path)
			}
			fileInfo, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat %q: %v", path, err)
			}
			if got := fileInfo.Mode().Perm(); got != 0o600 {
				t.Errorf("file %q mode = %o, want 600", path, got)
			}
		}

		spool, err := os.ReadFile(listenerStore.SpoolPath(agent.id))
		if err != nil {
			t.Fatalf("read spool for %s: %v", agent.id, err)
		}
		if !strings.Contains(string(spool), agent.notification) {
			t.Errorf("spool for %s does not contain its notification: %q", agent.id, spool)
		}
		other := agents[1-index]
		if strings.Contains(string(spool), other.notification) {
			t.Errorf("spool for %s contains %s's notification", agent.id, other.id)
		}
	}

	for _, oldPath := range []string{
		filepath.Join(home, ".aircommand", "credentials.json"),
		filepath.Join(home, ".aircommand", "state"),
		filepath.Join(home, ".aircommand", "spool"),
	} {
		if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("old shared path %q exists or could not be checked: %v", oldPath, err)
		}
	}
}

func TestStoragePathsConfineCraftedAgentID(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	credentialStore := credentials.NewStore(home)
	listenerStore := listenstore.NewStore(home)
	agentID := "../../other-agent"
	for _, path := range []string{
		credentialStore.Path(agentID),
		listenerStore.StatePath(agentID),
		listenerStore.SpoolPath(agentID),
	} {
		relative, err := filepath.Rel(storagepath.AgentsDirectory(home), path)
		if err != nil {
			t.Fatalf("Rel: %v", err)
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			t.Errorf("crafted agent ID escaped agents directory: %q", path)
		}
		if strings.Contains(relative, agentID) {
			t.Errorf("crafted agent ID was used directly in path %q", path)
		}
	}
}

func treeFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, relative)
		return nil
	})
	sort.Strings(files)
	return files, err
}
