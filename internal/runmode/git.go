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

// StartTokens binds synchronously before reconciliation can clone private
// repositories. The accept loop then lives and dies with the daemon context.
func StartTokens(ctx context.Context, home string, source *TokenSource) error {
	listener, err := listenTokens(home)
	if err != nil {
		return err
	}
	go func() { _ = serveTokenListener(ctx, home, listener, source) }()
	return nil
}

// ServeTokens is also useful for foreground integration tests.
func ServeTokens(ctx context.Context, home string, source *TokenSource) error {
	listener, err := listenTokens(home)
	if err != nil {
		return err
	}
	return serveTokenListener(ctx, home, listener, source)
}
func listenTokens(home string) (net.Listener, error) {
	path := TokenSocket(home)
	if len(path) > 103 {
		return nil, fmt.Errorf("run token socket path is too long")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		conn, e := net.DialTimeout("unix", path, 100*time.Millisecond)
		if e == nil {
			conn.Close()
			return nil, fmt.Errorf("run token daemon already running")
		}
		if e = os.Remove(path); e != nil {
			return nil, e
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0o600); err != nil {
		listener.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return listener, nil
}
func serveTokenListener(ctx context.Context, home string, listener net.Listener, source *TokenSource) error {
	defer listener.Close()
	defer os.Remove(TokenSocket(home))
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
