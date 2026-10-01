package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// migrationSource returns the newest legacy session in this work folder.
// A fixed-ID session already present always wins: repeating --fork would
// overwrite an agent's conversation after a crash before daemon.json sync.
func migrationSource(workDir, agentID string) string {
	root, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join(sessionDir(root, workDir), "*.jsonl"))
	var latest string
	var newest time.Time
	for _, path := range files {
		if strings.HasSuffix(path, "_"+agentID+".jsonl") {
			return ""
		}
		info, err := os.Stat(path)
		if err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
			latest = path
		}
	}
	return latest
}

func sessionDir(root, workDir string) string {
	// pi's project session directory replaces both separators and colons.
	encoded := strings.NewReplacer(string(filepath.Separator), "-", ":", "-").Replace(strings.Trim(filepath.Clean(workDir), string(filepath.Separator)))
	return filepath.Join(root, ".pi", "agent", "sessions", "--"+encoded+"--")
}
func fixedSessionExists(workDir, agentID string) bool {
	root, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	files, _ := filepath.Glob(filepath.Join(sessionDir(root, workDir), "*_"+agentID+".jsonl"))
	return len(files) != 0
}
