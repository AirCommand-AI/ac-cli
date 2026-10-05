package app

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
	"github.com/AirCommand-AI/ac-cli/internal/daemonclient"
	"github.com/AirCommand-AI/ac-cli/internal/listenstore"
)

func stripListenerTimestamps(output string) string { return output }
func listenerApp(baseURL, home string) (*App, *bytes.Buffer, *bytes.Buffer) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	return &App{BaseURL: baseURL, HTTPClient: http.DefaultClient, Store: credentials.NewStore(home), ListenStore: listenstore.NewStore(home), Stdout: stdout, Stderr: stderr, SessionClient: &fakeSessionControl{messages: []daemonclient.SessionMessage{{Type: "detached"}}}, ProcessSnapshot: func(pid int) (int, string, string, error) {
		return 1, "node /opt/pi-coding-agent/dist/cli.js", "start", nil
	}}, stdout, stderr
}
func TestListenIsDaemonPipeAndNeverContactsServer(t *testing.T) {
	client, out, errOut := listenerApp("http://127.0.0.1:1", t.TempDir())
	credential := testCredential()
	saveTestCredential(t, client, credential)
	client.SessionClient.(*fakeSessionControl).messages = []daemonclient.SessionMessage{{Type: "connect", AgentID: credential.AgentID, Workstream: credential.WorkstreamCode}, {Type: "wake", Line: "Task 7 assigned to you"}, {Type: "detached"}}
	if code := client.Run([]string{"listen", "--workstream", credential.WorkstreamCode, "--agent", credential.AgentID}); code != 0 {
		t.Fatalf("listen failed: %s", errOut.String())
	}
	if !strings.Contains(out.String(), "Task 7 assigned to you") {
		t.Fatalf("wake missing: %q", out.String())
	}
}
