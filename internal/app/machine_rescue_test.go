package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func TestMachineRescueOpened(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/organizations":
			_, _ = w.Write([]byte(`{"organizations":[{"organizationId":"org_aaaaaaaaaaaaaaaaaaaaaaaaaa","name":"Air Command"}]}`))
		case "/v1/workstreams/475/machine-runs/" + testMachineRunID + "/rescue-opened":
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer machine-token" || r.Header.Get(organizationHeader) != "org_aaaaaaaaaaaaaaaaaaaaaaaaaa" {
				t.Errorf("method=%s headers=%v", r.Method, r.Header)
			}
			w.WriteHeader(status)
			if status != http.StatusOK {
				_, _ = w.Write([]byte(`{"message":"You are not in this workstream","code":"Forbidden"}`))
				return
			}
			_, _ = w.Write([]byte(`{"runId":"` + testMachineRunID + `"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	tests := []struct {
		name    string
		args    []string
		status  int
		ok      bool
		wantOut string
		wantErr string
	}{
		{name: "posts notice", args: []string{"--workspace", "Air Command", "--workstream", "475", "--run", testMachineRunID}, status: http.StatusOK, ok: true, wantOut: "rescue shell opened on " + testMachineRunID},
		{name: "server refuses", args: []string{"--workspace", "Air Command", "--workstream", "475", "--run", testMachineRunID}, status: http.StatusForbidden, wantErr: "HTTP 403): You are not in this workstream"},
		{name: "missing workspace", args: []string{"--workstream", "475", "--run", testMachineRunID}, wantErr: "machine rescue-opened"},
		{name: "bad run", args: []string{"--workspace", "Air Command", "--workstream", "475", "--run", "x"}, wantErr: "--run must be"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status = tc.status
			a, out, errOut := testApp(t, server.URL, "", nil)
			if err := a.Store.SaveMachine(credentials.Machine{APIToken: "machine-token", DeviceID: "dev-1"}); err != nil {
				t.Fatal(err)
			}
			code := a.Run(append([]string{"machine", "rescue-opened"}, tc.args...))
			if (code == 0) != tc.ok {
				t.Fatalf("code=%d stderr=%s", code, errOut.String())
			}
			if !strings.Contains(out.String(), tc.wantOut) || !strings.Contains(errOut.String(), tc.wantErr) {
				t.Fatalf("stdout=%s stderr=%s", out.String(), errOut.String())
			}
		})
	}
}
