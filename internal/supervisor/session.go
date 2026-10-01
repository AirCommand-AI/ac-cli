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
	name := "--" + strings.ReplaceAll(strings.Trim(filepath.Clean(workDir), string(filepath.Separator)), string(filepath.Separator), "-") + "--"
	files, _ := filepath.Glob(filepath.Join(root, ".pi", "agent", "sessions", name, "*.jsonl"))
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
