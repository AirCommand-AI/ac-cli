package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

const machineFileVersion = 1

// ErrNoMachineLogin reports that this machine has not been logged in. Callers
// must tell the operator to run login rather than attempting one themselves.
var ErrNoMachineLogin = errors.New("this machine is not logged in to AirCommand")

// Machine is the organization-scoped credential a human approved in a browser.
// It is not an agent credential: it can list and read workstreams and join
// them, but it cannot send messages, post updates, or write tasks.
type Machine struct {
	Version        int    `json:"version"`
	APIToken       string `json:"apiToken"`
	OrganizationID string `json:"organizationId"`
	CreatedAt      string `json:"createdAt"`
}

// MachinePath is the single machine credential location for this home.
func (s *Store) MachinePath() string {
	return filepath.Join(storagepath.Root(s.home), "machine.json")
}

// SaveMachine writes the machine credential with owner-only permissions,
// replacing any existing login.
func (s *Store) SaveMachine(machine Machine) error {
	if machine.APIToken == "" || machine.OrganizationID == "" {
		return errors.New("machine credential is incomplete")
	}
	machine.Version = machineFileVersion
	directory := storagepath.Root(s.home)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create storage directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("secure storage directory: %w", err)
	}
	return s.write(directory, s.MachinePath(), machine)
}

// LoadMachine reads the machine credential. It returns ErrNoMachineLogin when
// this machine has never been logged in.
func (s *Store) LoadMachine() (Machine, error) {
	contents, err := os.ReadFile(s.MachinePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Machine{}, ErrNoMachineLogin
		}
		return Machine{}, fmt.Errorf("read machine credential: %w", err)
	}
	var machine Machine
	if err := json.Unmarshal(contents, &machine); err != nil {
		return Machine{}, fmt.Errorf("decode machine credential: %w", err)
	}
	if machine.APIToken == "" || machine.OrganizationID == "" {
		return Machine{}, ErrNoMachineLogin
	}
	return machine, nil
}

// DeleteMachine removes the machine credential. It is not an error to delete a
// login that is not present.
func (s *Store) DeleteMachine() error {
	if err := os.Remove(s.MachinePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove machine credential: %w", err)
	}
	return nil
}

// LocalAgent is the non-secret description of one enrolled agent on this
// machine. It deliberately carries no token or key.
type LocalAgent struct {
	AgentID        string
	WorkstreamCode string
}

// ListLocalAgents reports the agents enrolled on this machine. It reads only
// the agent ID and workstream code from each stored credential and never
// returns secret material. A credential that cannot be read is skipped rather
// than failing the caller, because this exists to annotate a listing.
func (s *Store) ListLocalAgents() []LocalAgent {
	entries, err := os.ReadDir(storagepath.AgentsDirectory(s.home))
	if err != nil {
		return nil
	}
	var agents []LocalAgent
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		agentID, ok := storagepath.ValueFromFilenameComponent(entry.Name())
		if !ok {
			continue
		}
		file, present, err := loadFile(filepath.Join(storagepath.AgentsDirectory(s.home), entry.Name(), "credentials.json"))
		if err != nil || !present {
			continue
		}
		for storedID, credential := range file.Agents {
			if storedID != agentID {
				continue
			}
			agents = append(agents, LocalAgent{AgentID: agentID, WorkstreamCode: credential.WorkstreamCode})
		}
	}
	return agents
}
