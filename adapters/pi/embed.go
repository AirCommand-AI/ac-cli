package piadapter

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Source is the pi extension shipped with this aircom binary.
//
//go:embed index.ts
var Source []byte

func Path(home string) string {
	return filepath.Join(home, ".pi", "agent", "extensions", "aircommand", "index.ts")
}

// Sync replaces only the global extension. A project-local extension is deliberately untouched.
func Sync(home string) error {
	path := Path(home)
	old, err := os.ReadFile(path)
	if err == nil && bytes.Equal(old, Source) {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read pi extension: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".aircommand-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(Source); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return nil
}

// VerifyHeadless checks the installed extension before pi receives the custom CLI flag.
func VerifyHeadless(home string) error {
	path := Path(home)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("pi extension at %s unavailable: %w; run aircom daemon start to sync it", path, err)
	}
	if !strings.Contains(string(data), `"aircommand-headless"`) || !strings.Contains(string(data), "registerFlag(HEADLESS_FLAG") {
		return fmt.Errorf("pi extension at %s does not register --aircommand-headless; run aircom daemon start to sync it", path)
	}
	return nil
}
