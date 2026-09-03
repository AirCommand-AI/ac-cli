package storagepath

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const encodedComponentPrefix = "id-"

var safeFilenameComponent = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type LegacyLayoutError struct {
	Paths []string
}

func (e *LegacyLayoutError) Error() string {
	return "legacy AirCommand storage layout found"
}

func Root(home string) string {
	return filepath.Join(home, ".aircommand")
}

func AgentsDirectory(home string) string {
	return filepath.Join(Root(home), "agents")
}

func AgentDirectory(home string, agentID string) string {
	return filepath.Join(AgentsDirectory(home), FilenameComponent(agentID))
}

// FilenameComponent maps an identifier to one non-traversing, collision-free
// filename component. Ordinary identifiers remain readable; reserved or unsafe
// values use unpadded URL-safe base64.
func FilenameComponent(value string) string {
	if value != "." && value != ".." && safeFilenameComponent.MatchString(value) && !strings.HasPrefix(value, encodedComponentPrefix) {
		return value
	}
	return encodedComponentPrefix + base64.RawURLEncoding.EncodeToString([]byte(value))
}

func ValueFromFilenameComponent(component string) (string, bool) {
	if !strings.HasPrefix(component, encodedComponentPrefix) {
		return component, FilenameComponent(component) == component
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(component, encodedComponentPrefix))
	if err != nil {
		return "", false
	}
	value := string(decoded)
	return value, FilenameComponent(value) == component
}

func CheckLegacyLayout(home string) error {
	root := Root(home)
	candidates := []string{
		filepath.Join(root, "credentials.json"),
		filepath.Join(root, "state"),
		filepath.Join(root, "spool"),
	}

	var found []string
	for _, candidate := range candidates {
		_, err := os.Lstat(candidate)
		switch {
		case err == nil:
			found = append(found, candidate)
		case errors.Is(err, os.ErrNotExist):
			continue
		default:
			return fmt.Errorf("inspect legacy AirCommand storage: %w", err)
		}
	}
	if len(found) != 0 {
		return &LegacyLayoutError{Paths: found}
	}
	return nil
}

func EnsureAgentDirectory(home string, agentID string) (string, error) {
	if err := CheckLegacyLayout(home); err != nil {
		return "", err
	}

	directories := []string{Root(home), AgentsDirectory(home), AgentDirectory(home, agentID)}
	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return "", fmt.Errorf("create AirCommand data directory: %w", err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return "", fmt.Errorf("secure AirCommand data directory: %w", err)
		}
	}
	return directories[len(directories)-1], nil
}
