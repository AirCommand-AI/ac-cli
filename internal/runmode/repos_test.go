package runmode

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func TestReportRepo(t *testing.T) {
	cases := []struct {
		name     string
		ok       bool
		reason   string
		status   int
		wantBody string
		wantErr  bool
	}{
		{"success", true, "", http.StatusNoContent, `{"agentId":"agm_1","repo":"owner/name","ok":true}`, false},
		{"failure", false, "clone failed", http.StatusNoContent, `{"agentId":"agm_1","repo":"owner/name","ok":false,"error":"clone failed"}`, false},
		{"older dashboard", true, "", http.StatusNotFound, `{"agentId":"agm_1","repo":"owner/name","ok":true}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := credentials.NewStore(t.TempDir())
			if err := store.SaveMachine(credentials.Machine{DeviceID: "dev", APIToken: "machine"}); err != nil {
				t.Fatal(err)
			}
			var body string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/agent/v1/machines/me/run/repos" || r.Header.Get("Authorization") != "Bearer machine" {
					t.Errorf("request %s %s bearer %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
				}
				raw, _ := io.ReadAll(r.Body)
				body = string(raw)
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			api := API{BaseURL: server.URL, Client: server.Client(), Store: store}
			err := api.ReportRepo(context.Background(), "agm_1", "owner/name", tc.ok, tc.reason)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			var status *HTTPError
			if tc.wantErr && (!errors.As(err, &status) || status.HTTPStatus() != http.StatusNotFound) {
				t.Fatalf("err=%v", err)
			}
			if body != tc.wantBody {
				t.Fatalf("body=%s want %s", body, tc.wantBody)
			}
		})
	}
}
