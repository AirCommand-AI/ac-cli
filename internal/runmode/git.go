package runmode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/storagepath"
)

// TokenSocket is separate from the daemon control socket. Only the local
// unprivileged agent user can connect; no access token is passed on argv.
func TokenSocket(home string) string {
	return filepath.Join(storagepath.DaemonDirectory(home), "github.sock")
}
func writeGHHosts(home, token string) error {
	// This is a throwaway run home; keep the installed token only in private files.
	return writePrivate(filepath.Join(home, ".config", "gh", "hosts.yml"), []byte("github.com:\n    oauth_token: "+token+"\n    git_protocol: https\n"))
}

// ServeTokens lives and dies with the machine daemon. Renewal is serialized
// by TokenSource; a malformed request never leaks the token.
func ServeTokens(ctx context.Context, home string, source *TokenSource) error {
	path := TokenSocket(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(path)
	if err = os.Chmod(path, 0o600); err != nil {
		return err
	}
	go func() { <-ctx.Done(); _ = listener.Close() }()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
			var request struct {
				Op string `json:"op"`
			}
			if json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&request) != nil || request.Op != "github-token" {
				return
			}
			token, e := source.Get(ctx, false)
			if e != nil {
				_ = json.NewEncoder(conn).Encode(map[string]any{"ok": false})
				return
			}
			_ = json.NewEncoder(conn).Encode(map[string]any{"ok": true, "token": token})
		}()
	}
}

func GitCredential(ctx context.Context, home string, in io.Reader, out io.Writer) error {
	// Git passes a protocol/host pair on stdin. Never return a token for an
	// unrelated host (including attacker-controlled credential requests).
	values := make(map[string]string)
	scan := bufio.NewScanner(io.LimitReader(in, 4096))
	for scan.Scan() {
		line := scan.Text()
		if line == "" {
			break
		}
		k, v, found := strings.Cut(line, "=")
		if found {
			values[k] = v
		}
	}
	if err := scan.Err(); err != nil {
		return err
	}
	if values["protocol"] != "https" || values["host"] != "github.com" {
		return nil
	}
	dial := net.Dialer{}
	conn, err := dial.DialContext(ctx, "unix", TokenSocket(home))
	if err != nil {
		return fmt.Errorf("run token daemon unavailable: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	if err = json.NewEncoder(conn).Encode(map[string]string{"op": "github-token"}); err != nil {
		return err
	}
	var reply struct {
		OK    bool   `json:"ok"`
		Token string `json:"token"`
	}
	if err = json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&reply); err != nil {
		return err
	}
	if !reply.OK || reply.Token == "" {
		return fmt.Errorf("run token unavailable")
	}
	_, err = fmt.Fprintf(out, "protocol=https\nhost=github.com\nusername=x-access-token\npassword=%s\n\n", reply.Token)
	return err
}
