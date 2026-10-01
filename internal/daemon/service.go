package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/adapters/pi"
	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

func SystemdUnit(aircom, tmux, pi, path string) string {
	return fmt.Sprintf("[Unit]\nDescription=AirCommand agent daemon\n\n[Service]\nType=simple\nExecStart=%s daemon run --tmux %s --pi %s\nEnvironment=PATH=%s\nKillMode=process\nRestart=on-failure\n\n[Install]\nWantedBy=default.target\n", systemdEscape(aircom), systemdEscape(tmux), systemdEscape(pi), systemdEscape(path))
}
func systemdEscape(value string) string {
	// systemd requires quoting whitespace, literal percent signs and backslashes.
	value = strings.ReplaceAll(value, "%", "%%")
	return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
}
func LaunchdPlist(aircom, tmux, pi, path, log string) string {
	var b bytes.Buffer
	esc := func(value string) string {
		var out bytes.Buffer
		_ = xml.EscapeText(&out, []byte(value))
		return out.String()
	}
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n<key>Label</key><string>ai.aircommand.daemon</string>\n<key>ProgramArguments</key><array>\n")
	for _, arg := range []string{aircom, "daemon", "run", "--tmux", tmux, "--pi", pi} {
		fmt.Fprintf(&b, "<string>%s</string>\n", esc(arg))
	}
	fmt.Fprintf(&b, "</array>\n<key>EnvironmentVariables</key><dict><key>PATH</key><string>%s</string></dict>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n<key>StandardOutPath</key><string>%s</string>\n<key>StandardErrorPath</key><string>%s</string>\n</dict></plist>\n", esc(path), esc(log), esc(log))
	return b.String()
}

// Service coordinates the OS user manager. Runner is replaceable in tests.
type Runner func(context.Context, string, ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type Service struct {
	Home   string
	OS     string
	Run    Runner
	Path   string
	Aircom string
	Tmux   string
	Pi     string
}

func (s Service) platform() string {
	if s.OS != "" {
		return s.OS
	}
	return runtime.GOOS
}
func (s Service) runner() Runner {
	if s.Run != nil {
		return s.Run
	}
	return execRunner
}
func (s Service) paths() (string, string, string, error) {
	paths := []string{s.Aircom, s.Tmux, s.Pi}
	names := []string{"aircom", "tmux", "pi"}
	for i, p := range paths {
		if p == "" {
			var err error
			p, err = exec.LookPath(names[i])
			if err != nil {
				return "", "", "", err
			}
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", "", "", err
		}
		paths[i] = abs
	}
	return paths[0], paths[1], paths[2], nil
}
func (s Service) envPath() string {
	if s.Path != "" {
		return s.Path
	}
	return os.Getenv("PATH")
}
func (s Service) Start(ctx context.Context) error {
	if err := piadapter.Sync(s.Home); err != nil {
		return fmt.Errorf("sync pi extension: %w", err)
	}
	aircom, tmux, pi, err := s.paths()
	if err != nil {
		return err
	}
	run := s.runner()
	switch s.platform() {
	case "linux":
		// Non-lingering user managers disappear on logout, losing boot recovery.
		current, err := user.Current()
		if err != nil {
			return err
		}
		output, err := run(ctx, "loginctl", "show-user", current.Username, "--property=Linger", "--value")
		if err != nil || strings.TrimSpace(string(output)) != "yes" {
			return fmt.Errorf("enable user lingering with loginctl enable-linger before starting the daemon")
		}
		file := filepath.Join(s.Home, ".config/systemd/user/aircom-daemon.service")
		if err := writeService(file, SystemdUnit(aircom, tmux, pi, s.envPath())); err != nil {
			return err
		}
		if _, err := run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		if _, err = run(ctx, "systemctl", "--user", "enable", "--now", "aircom-daemon.service"); err != nil {
			return err
		}
		if running, ok := s.runningVersion(ctx); ok && running != AircomVersion {
			// KillMode=process: restarting replaces only the daemon; agents keep
			// running and are re-adopted by the new version.
			_, err = run(ctx, "systemctl", "--user", "restart", "aircom-daemon.service")
		}
		return err
	case "darwin":
		file := filepath.Join(s.Home, "Library/LaunchAgents/ai.aircommand.daemon.plist")
		content := LaunchdPlist(aircom, tmux, pi, s.envPath(), storagepath.DaemonLog(s.Home))
		previous, readErr := os.ReadFile(file)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		running, ok := s.runningVersion(ctx)
		if ok && bytes.Equal(previous, []byte(content)) {
			if running == AircomVersion {
				return nil // The running daemon already has this exact service definition.
			}
			// Same definition, older binary: SIGTERM leaves agents running and the
			// restarted daemon re-adopts them.
			_, err = run(ctx, "launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d", os.Getuid())+"/ai.aircommand.daemon")
			return err
		}
		if err := writeService(file, content); err != nil {
			return err
		}
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		_, _ = run(ctx, "launchctl", "bootout", domain, file)
		// RunAtLoad starts the daemon on bootstrap; -k would kill a healthy daemon.
		_, err = run(ctx, "launchctl", "bootstrap", domain, file)
		return err
	default:
		return fmt.Errorf("daemon service is unsupported on %s", s.platform())
	}
}
func (s Service) Stop(ctx context.Context) error {
	// Always tell the daemon to stop its agents before disabling the service.
	if _, err := Call(ctx, s.Home, Request{Op: "shutdown", StopAgents: true}); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	run := s.runner()
	switch s.platform() {
	case "linux":
		_, err := run(ctx, "systemctl", "--user", "disable", "--now", "aircom-daemon.service")
		return err
	case "darwin":
		_, err := run(ctx, "launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), filepath.Join(s.Home, "Library/LaunchAgents/ai.aircommand.daemon.plist"))
		return err
	default:
		return fmt.Errorf("daemon service is unsupported on %s", s.platform())
	}
}
func writeService(file, content string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(content), 0600)
}

// runningVersion asks a running daemon for the aircom release it was built as.
// ok is false when no daemon answers.
func (s Service) runningVersion(ctx context.Context) (string, bool) {
	statusCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	data, err := Call(statusCtx, s.Home, Request{Op: "status"})
	if err != nil {
		return "", false
	}
	var status struct {
		AircomVersion string `json:"aircomVersion"`
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return "", true
	}
	return status.AircomVersion, true
}
