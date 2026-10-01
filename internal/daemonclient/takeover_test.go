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

func TestTakeoverKeepsForegroundWhenControlFenceLost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake-pi")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	defer server.Close()
	closed := make(chan struct{})
	c := Client{SocketPath: "unused", Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	go func() {
		defer server.Close()
		defer close(closed)
		decoder := json.NewDecoder(server)
		encoder := json.NewEncoder(server)
		var req map[string]any
		_ = decoder.Decode(&req)
		_ = encoder.Encode(map[string]any{"ok": true, "data": map[string]any{"piPath": path, "workDir": ".", "sessionId": "test", "args": []string{}}})
		var pid map[string]any
		_ = decoder.Decode(&pid)
	}()
	done := make(chan error, 1)
	stderr := new(bytes.Buffer)
	go func() {
		done <- c.Takeover(context.Background(), "agent", bytes.NewBuffer(nil), &bytes.Buffer{}, stderr)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("fake daemon never received pid")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("foreground pi was killed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("foreground pi hung")
	}
	// A dropped daemon connection must not kill the foreground process.
}
