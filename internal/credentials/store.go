package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

const fileVersion = 1

type Credential struct {
	APIToken       string `json:"apiToken"`
	SocketKey      string `json:"socketKey"`
	WorkstreamCode string `json:"workstreamCode"`
	AgentID        string `json:"agentId"`
	SocketAddress  string `json:"socketAddress"`
	// AgentName is the name this agent answers to in its workstream. It is
	// optional because credentials written before joining existed do not
	// carry it, and it is never used for authentication -- only to recognise
	// an agent this machine already owns and to label it for a human.
	AgentName string `json:"agentName,omitempty"`
	// OrganizationID is the organization the agent joined in. Workstream codes
	// are unique only within an organization, so this plus the code is what
	// identifies the workstream. Credentials written before joins recorded it,
	// or by the older setup-link exchange, leave it empty.
	OrganizationID string `json:"organizationId,omitempty"`
}

// WorkstreamKey identifies the workstream this credential belongs to, across
// organizations. It tags state that is only meaningful in that workstream,
// such as the listener's cursor.
func (c Credential) WorkstreamKey() string {
	return c.OrganizationID + "/" + c.WorkstreamCode
}

type File struct {
	Version int                   `json:"version"`
	Agents  map[string]Credential `json:"agents"`
}

type Store struct {
	home string
}

type MultipleAgentsError struct {
	AgentIDs []string
}

func (e *MultipleAgentsError) Error() string {
	return "multiple local agents require explicit selection"
}

func NewStore(home string) *Store {
	return &Store{home: home}
}

// Home is the storage root this store was built for. Callers that need to
// reach sibling per-agent files, such as the agent lock, resolve them from it
// rather than guessing at the user's home directory again.
func (s *Store) Home() string { return s.home }

func (s *Store) Path(agentID string) string {
	return filepath.Join(storagepath.AgentDirectory(s.home, agentID), "credentials.json")
}

// Delete removes one agent's workstream credential. Only that file goes: the
// agent's directory also holds its lock, listener cursor and spool, which a
// listener still running in another process may be using. It is not an error
// to delete a credential that is not present.
func (s *Store) Delete(agentID string) error {
	if err := os.Remove(s.Path(agentID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove agent credential: %w", err)
	}
	return nil
}

func (s *Store) CheckLayout() error {
	return storagepath.CheckLegacyLayout(s.home)
}

func (s *Store) Save(credential Credential) error {
	if credential.AgentID == "" || credential.WorkstreamCode == "" || credential.APIToken == "" || credential.SocketKey == "" || credential.SocketAddress == "" {
		return errors.New("credential is incomplete")
	}
	directory, err := storagepath.EnsureAgentDirectory(s.home, credential.AgentID)
	if err != nil {
		return err
	}

	file, err := s.loadAgent(credential.AgentID)
	if err != nil {
		return err
	}
	if len(file.Agents) > 1 {
		return errors.New("per-agent credentials file contains multiple agents")
	}
	for storedAgentID := range file.Agents {
		if storedAgentID != credential.AgentID {
			return errors.New("per-agent credentials file belongs to another agent")
		}
	}
	file.Agents[credential.AgentID] = credential
	return s.write(directory, s.Path(credential.AgentID), file)
}

func (s *Store) FindByWorkstream(workstreamCode string) (Credential, error) {
	if err := s.CheckLayout(); err != nil {
		return Credential{}, err
	}
	entries, err := os.ReadDir(storagepath.AgentsDirectory(s.home))
	if errors.Is(err, os.ErrNotExist) {
		return Credential{}, errors.New("no credentials for workstream")
	}
	if err != nil {
		return Credential{}, fmt.Errorf("read agent directories: %w", err)
	}

	var agentDirectories []string
	for _, entry := range entries {
		if entry.IsDir() {
			agentDirectories = append(agentDirectories, entry.Name())
		}
	}
	if len(agentDirectories) == 0 {
		return Credential{}, errors.New("no credentials for workstream")
	}
	if len(agentDirectories) > 1 {
		agentIDs := make([]string, 0, len(agentDirectories))
		for _, directory := range agentDirectories {
			agentID, ok := storagepath.ValueFromFilenameComponent(directory)
			if !ok {
				agentID = directory
			}
			agentIDs = append(agentIDs, agentID)
		}
		sort.Strings(agentIDs)
		return Credential{}, &MultipleAgentsError{AgentIDs: agentIDs}
	}

	directory := agentDirectories[0]
	path := filepath.Join(storagepath.AgentsDirectory(s.home), directory, "credentials.json")
	file, found, err := loadFile(path)
	if err != nil {
		return Credential{}, err
	}
	if !found {
		return Credential{}, errors.New("no credentials for workstream")
	}
	credential, err := credentialFromFile(file, directory)
	if err != nil {
		return Credential{}, err
	}
	if credential.WorkstreamCode != workstreamCode {
		return Credential{}, errors.New("no credentials for workstream")
	}
	return credential, nil
}

func (s *Store) FindByAgent(workstreamCode string, agentID string) (Credential, error) {
	if err := s.CheckLayout(); err != nil {
		return Credential{}, err
	}
	file, found, err := loadFile(s.Path(agentID))
	if err != nil {
		return Credential{}, err
	}
	if !found {
		return Credential{}, errors.New("no credentials for agent in workstream")
	}
	credential, err := credentialFromFile(file, storagepath.FilenameComponent(agentID))
	if err != nil {
		return Credential{}, err
	}
	if credential.AgentID != agentID || credential.WorkstreamCode != workstreamCode {
		return Credential{}, errors.New("no credentials for agent in workstream")
	}
	return credential, nil
}

func (s *Store) loadAgent(agentID string) (File, error) {
	file, found, err := loadFile(s.Path(agentID))
	if err != nil {
		return File{}, err
	}
	if !found {
		return File{Version: fileVersion, Agents: make(map[string]Credential)}, nil
	}
	return file, nil
}

func loadFile(path string) (File, bool, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return File{}, false, nil
	}
	if err != nil {
		return File{}, false, fmt.Errorf("read credentials: %w", err)
	}

	var file File
	if err := json.Unmarshal(contents, &file); err != nil {
		return File{}, false, errors.New("credentials file is invalid")
	}
	if file.Version != fileVersion {
		return File{}, false, fmt.Errorf("unsupported credentials file version %d", file.Version)
	}
	if file.Agents == nil {
		return File{}, false, errors.New("credentials file has no agents")
	}
	return file, true, nil
}

func credentialFromFile(file File, directoryName string) (Credential, error) {
	if len(file.Agents) != 1 {
		return Credential{}, errors.New("per-agent credentials file must contain exactly one agent")
	}
	for agentID, credential := range file.Agents {
		if credential.AgentID != agentID {
			return Credential{}, errors.New("credential agent ID does not match its key")
		}
		if storagepath.FilenameComponent(agentID) != directoryName {
			return Credential{}, errors.New("credential agent ID does not match its directory")
		}
		return credential, nil
	}
	panic("unreachable")
}

// write atomically encodes any credential document with owner-only
// permissions.
func (s *Store) write(directory string, path string, file any) error {
	temporary, err := os.CreateTemp(directory, ".credentials-*")
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace credentials: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure credentials: %w", err)
	}
	return nil
}
