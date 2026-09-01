package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const fileVersion = 1

type Credential struct {
	APIToken       string `json:"apiToken"`
	SocketKey      string `json:"socketKey"`
	WorkstreamCode string `json:"workstreamCode"`
	AgentID        string `json:"agentId"`
	SocketAddress  string `json:"socketAddress"`
}

type File struct {
	Version int                   `json:"version"`
	Agents  map[string]Credential `json:"agents"`
}

type Store struct {
	directory string
	path      string
}

func NewStore(home string) *Store {
	directory := filepath.Join(home, ".aircommand")
	return &Store{
		directory: directory,
		path:      filepath.Join(directory, "credentials.json"),
	}
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Save(credential Credential) error {
	if credential.AgentID == "" || credential.WorkstreamCode == "" || credential.APIToken == "" || credential.SocketKey == "" || credential.SocketAddress == "" {
		return errors.New("credential is incomplete")
	}
	if err := os.MkdirAll(s.directory, 0o700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	if err := os.Chmod(s.directory, 0o700); err != nil {
		return fmt.Errorf("secure credential directory: %w", err)
	}

	file, err := s.load()
	if err != nil {
		return err
	}
	file.Agents[credential.AgentID] = credential
	return s.write(file)
}

func (s *Store) FindByWorkstream(workstreamCode string) (Credential, error) {
	file, err := s.load()
	if err != nil {
		return Credential{}, err
	}

	var found Credential
	matches := 0
	for agentID, credential := range file.Agents {
		if credential.AgentID != agentID {
			return Credential{}, errors.New("credential agent ID does not match its key")
		}
		if credential.WorkstreamCode == workstreamCode {
			found = credential
			matches++
		}
	}
	if matches == 0 {
		return Credential{}, errors.New("no credentials for workstream")
	}
	if matches > 1 {
		return Credential{}, errors.New("multiple credentials for workstream")
	}
	return found, nil
}

func (s *Store) load() (File, error) {
	contents, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return File{Version: fileVersion, Agents: make(map[string]Credential)}, nil
	}
	if err != nil {
		return File{}, fmt.Errorf("read credentials: %w", err)
	}

	var file File
	if err := json.Unmarshal(contents, &file); err != nil {
		return File{}, errors.New("credentials file is invalid")
	}
	if file.Version != fileVersion {
		return File{}, fmt.Errorf("unsupported credentials file version %d", file.Version)
	}
	if file.Agents == nil {
		return File{}, errors.New("credentials file has no agents")
	}
	return file, nil
}

func (s *Store) write(file File) error {
	temporary, err := os.CreateTemp(s.directory, ".credentials-*")
	if err != nil {
		return fmt.Errorf("create temporary credential file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary credential file: %w", err)
	}

	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(file); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode credentials: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync credentials: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close credentials: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace credentials: %w", err)
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("secure credentials: %w", err)
	}
	return nil
}
