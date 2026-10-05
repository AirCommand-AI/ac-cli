//go:build darwin

package peercred

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestDarwinPeerPIDAndDescendant(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "acp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	listener, err := net.Listen("unix", filepath.Join(dir, "s.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("unix", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	pid, err := PeerPID(server)
	if err != nil {
		t.Fatal(err)
	}
	if pid != os.Getpid() || !Descendant(pid, os.Getpid()) {
		t.Fatalf("peer pid %d, self %d", pid, os.Getpid())
	}
	if Descendant(pid, 99999999) {
		t.Fatal("invented ancestor accepted")
	}
}
