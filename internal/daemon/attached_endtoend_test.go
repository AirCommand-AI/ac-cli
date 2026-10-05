package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/agentstate"
	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	sup "github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

func TestAttachedAddonEventReportsK1ToFakeServer(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := credentials.NewStore(home)
	if err := store.Save(credentials.Credential{AgentID: "agm_e2e", WorkstreamCode: "478", APIToken: "secret", SocketAddress: "ac:agm_e2e"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var reports []agentstate.State
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/agent/v1/workstreams/478/agents/me/state" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("K1 request %s %s %s", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var state agentstate.State
		if err := json.NewDecoder(r.Body).Decode(&state); err != nil {
			t.Error(err)
		}
		mu.Lock()
		reports = append(reports, state)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	m := sup.New(home, "pi", "aircom", nil, signalPoll{})
	m.StateReport = func(ctx context.Context, d sup.AgentDefinition, s agentstate.State, at time.Time) error {
		return (agentstate.Reporter{BaseURL: server.URL, Client: server.Client(), Token: "secret"}).Report(ctx, d.Workstream, s, at)
	}
	listener, err := net.Listen("unix", filepath.Join(shortSocketDir(t), "e2e.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			c, e := listener.Accept()
			if e != nil {
				return
			}
			go serveConnection(ctx, c, m, time.Now(), "test", "", os.Getpid(), cancel, &atomic.Bool{}, nil)
		}
	}()
	req := func(v Request) map[string]any {
		t.Helper()
		conn, e := net.Dial("unix", listener.Addr().String())
		if e != nil {
			t.Fatal(e)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if e = json.NewEncoder(conn).Encode(v); e != nil {
			t.Fatal(e)
		}
		line, e := bufio.NewReader(conn).ReadBytes('\n')
		if e != nil {
			t.Fatal(e)
		}
		var reply map[string]any
		if e = json.Unmarshal(line, &reply); e != nil {
			t.Fatal(e)
		}
		return reply
	}
	// A real claim keeps the lock while the CLI does its join and attach.
	claim, err := net.Dial("unix", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer claim.Close()
	_ = claim.SetDeadline(time.Now().Add(3 * time.Second))
	cr := bufio.NewReader(claim)
	_ = json.NewEncoder(claim).Encode(Request{Op: "agent.claim", AgentID: "agm_e2e", Workstream: "478"})
	if _, err = cr.ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	_ = json.NewEncoder(claim).Encode(Request{Op: "session.attach", AgentID: "agm_e2e", Name: "addon", Workstream: "478", Program: "pi", SessionPID: os.Getpid(), SessionStart: sup.SessionProcessStart(os.Getpid()), SessionID: "conversation"})
	if _, err = cr.ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	if err = m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := req(Request{Op: "session.event", SessionPID: os.Getpid(), Kind: "run_start"})["ok"]; got != true {
		t.Fatalf("event rejected: %v", got)
	}
	if err = m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reports) < 2 || reports[len(reports)-1].Logical != agentstate.Working || reports[len(reports)-1].Physical != agentstate.Running {
		t.Fatalf("K1 reports after addon event: %+v", reports)
	}
}
