package listenstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

type Store struct {
	home string
}

// cursorState is how far an agent's listener has read. The cursor is a
// position in one workstream's notification feed, so it records which
// workstream it belongs to: after the agent leaves and joins another, the old
// position means nothing there and must not be sent as "since". The key is
// opaque here; callers pass the credential's workstream key (organization and
// code), since codes repeat across organizations.
type cursorState struct {
	Cursor     string `json:"cursor"`
	Workstream string `json:"workstream"`
}

func NewStore(home string) *Store {
	return &Store{home: home}
}

func (s *Store) StatePath(agentID string) string {
	return filepath.Join(storagepath.AgentDirectory(s.home, agentID), "state.json")
}

func (s *Store) SpoolPath(agentID string) string {
	return filepath.Join(storagepath.AgentDirectory(s.home, agentID), "spool.jsonl")
}

// LoadCursor returns the agent's stored cursor for workstream. A cursor saved
// for a different workstream, or one saved before cursors recorded their
// workstream, is reported as absent, so the listener takes a fresh baseline
// rather than skipping or misreading the new workstream's messages.
func (s *Store) LoadCursor(agentID string, workstream string) (string, bool, error) {
	if err := storagepath.CheckLegacyLayout(s.home); err != nil {
		return "", false, err
	}
	contents, err := os.ReadFile(s.StatePath(agentID))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read cursor state: %w", err)
	}

	var state cursorState
	if err := json.Unmarshal(contents, &state); err != nil {
		return "", false, errors.New("cursor state is invalid")
	}
	if state.Workstream == "" || state.Workstream != workstream {
		return "", false, nil
	}
	return state.Cursor, true, nil
}

// SaveCursor records how far the agent's listener has read in workstream.
func (s *Store) SaveCursor(agentID string, workstream string, cursor string) error {
	directory, err := storagepath.EnsureAgentDirectory(s.home, agentID)
	if err != nil {
		return err
	}

	temporary, err := os.CreateTemp(directory, ".cursor-*")
	if err != nil {
		return fmt.Errorf("create temporary cursor state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary cursor state: %w", err)
	}
	if err := json.NewEncoder(temporary).Encode(cursorState{Cursor: cursor, Workstream: workstream}); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode cursor state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync cursor state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close cursor state: %w", err)
	}

	path := s.StatePath(agentID)
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace cursor state: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure cursor state: %w", err)
	}
	return nil
}

func (s *Store) AppendNotification(agentID string, notification any) error {
	if _, err := storagepath.EnsureAgentDirectory(s.home, agentID); err != nil {
		return err
	}
	path := s.SpoolPath(agentID)

	encoded, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("encode notification: %w", err)
	}
	encoded = append(encoded, '\n')

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open notification spool: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure notification spool: %w", err)
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return fmt.Errorf("append notification spool: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync notification spool: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close notification spool: %w", err)
	}
	return nil
}
