package daemonclient

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTakeoverKillsForegroundWhenControlFenceLost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake-pi")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	defer server.Close()
	c := Client{SocketPath: "unused", Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	go func() {
		defer server.Close()
		decoder := json.NewDecoder(server)
		encoder := json.NewEncoder(server)
		var req map[string]any
		_ = decoder.Decode(&req)
		_ = encoder.Encode(map[string]any{"ok": true, "data": map[string]any{"piPath": path, "workDir": ".", "sessionId": "test", "args": []string{}}})
		var pid map[string]any
		_ = decoder.Decode(&pid)
	}()
	done := make(chan error, 1)
	go func() {
		done <- c.Takeover(context.Background(), "agent", bytes.NewBuffer(nil), &bytes.Buffer{}, &bytes.Buffer{})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("foreground pi survived lost control connection")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("foreground pi not killed after connection loss")
	}
}
