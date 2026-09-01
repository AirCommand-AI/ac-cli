package listenstore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var safeFilenameComponent = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type Store struct {
	root string
}

type cursorState struct {
	Cursor string `json:"cursor"`
}

func NewStore(home string) *Store {
	return &Store{root: filepath.Join(home, ".aircommand")}
}

func (s *Store) StatePath(workstreamCode string, agentID string) string {
	name := filenameComponent(workstreamCode) + "-" + filenameComponent(agentID) + ".json"
	return filepath.Join(s.root, "state", name)
}

func (s *Store) SpoolPath(workstreamCode string) string {
	return filepath.Join(s.root, "spool", filenameComponent(workstreamCode)+".jsonl")
}

func (s *Store) LoadCursor(workstreamCode string, agentID string) (string, error) {
	contents, err := os.ReadFile(s.StatePath(workstreamCode, agentID))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read cursor state: %w", err)
	}

	var state cursorState
	if err := json.Unmarshal(contents, &state); err != nil {
		return "", errors.New("cursor state is invalid")
	}
	return state.Cursor, nil
}

func (s *Store) SaveCursor(workstreamCode string, agentID string, cursor string) error {
	directory := filepath.Dir(s.StatePath(workstreamCode, agentID))
	if err := s.ensureDirectory(directory); err != nil {
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
	if err := json.NewEncoder(temporary).Encode(cursorState{Cursor: cursor}); err != nil {
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

	path := s.StatePath(workstreamCode, agentID)
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace cursor state: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure cursor state: %w", err)
	}
	return nil
}

func (s *Store) AppendNotification(workstreamCode string, notification any) error {
	path := s.SpoolPath(workstreamCode)
	if err := s.ensureDirectory(filepath.Dir(path)); err != nil {
		return err
	}

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

func (s *Store) ensureDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create AirCommand data directory: %w", err)
	}
	if err := os.Chmod(s.root, 0o700); err != nil {
		return fmt.Errorf("secure AirCommand data directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("secure AirCommand subdirectory: %w", err)
	}
	return nil
}

func filenameComponent(value string) string {
	if safeFilenameComponent.MatchString(value) {
		return value
	}
	return "id-" + base64.RawURLEncoding.EncodeToString([]byte(value))
}
