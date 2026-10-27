package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AirCommand-AI/ac-cli/internal/credentials"
)

func TestTokenRenewer(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		expiresAt  string
		status     int
		body       string
		wantCalls  int
		wantToken  string
		wantExpiry string
		wantErr    bool
	}{
		{"not due", "2026-11-20T12:00:00Z", 0, "", 0, "old-token", "2026-11-20T12:00:00Z", false},
		{"due renews", "2026-11-05T12:00:00Z", http.StatusCreated, `{"token":"new-token","expiresAt":"2026-12-01T12:00:00Z"}`, 1, "new-token", "2026-12-01T12:00:00Z", false},
		{"unknown expiry renews", "", http.StatusCreated, `{"token":"new-token","expiresAt":"2026-12-01T12:00:00Z"}`, 1, "new-token", "2026-12-01T12:00:00Z", false},
		{"too early records the expiry", "", http.StatusConflict, `{"expiresAt":"2026-11-20T12:00:00Z"}`, 1, "old-token", "2026-11-20T12:00:00Z", false},
		{"older dashboard", "", http.StatusNotFound, ``, 1, "old-token", "", false},
		{"expired credential", "2026-10-30T12:00:00Z", http.StatusUnauthorized, ``, 1, "old-token", "2026-10-30T12:00:00Z", true},
		{"invalid response", "", http.StatusCreated, `{"token":""}`, 1, "old-token", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/agent/v1/machines/me/token" || r.Header.Get("Authorization") != "Bearer old-token" {
					t.Errorf("request %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			store := credentials.NewStore(t.TempDir())
			if err := store.SaveMachine(credentials.Machine{APIToken: "old-token", DeviceID: "dev_0123456789abcdef01234567", MachineSocketSecret: "msock_keep", ExpiresAt: tc.expiresAt}); err != nil {
				t.Fatal(err)
			}
			renewer := &TokenRenewer{Store: store, Client: server.Client(), BaseURL: server.URL, Now: func() time.Time { return now }}
			if err := renewer.RenewIfDue(context.Background()); (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			machine, err := store.LoadMachine()
			if err != nil || calls != tc.wantCalls || machine.APIToken != tc.wantToken || machine.ExpiresAt != tc.wantExpiry || machine.MachineSocketSecret != "msock_keep" || machine.DeviceID != "dev_0123456789abcdef01234567" {
				t.Fatalf("calls %d machine %+v err %v", calls, machine, err)
			}
		})
	}
}
