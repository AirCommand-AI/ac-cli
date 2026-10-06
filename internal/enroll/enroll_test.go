package enroll

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestJoinAllowsFreshBearerForBoundAgent(t *testing.T) {
	var calls int
	request := func(method, path, token string, payload []byte) (Response, error) {
		calls++
		if method != http.MethodPost || path != "/v1/agents/agm_1/workstreams/348" || token != "device-token" {
			t.Fatalf("unexpected request %s %s %s", method, path, token)
		}
		var body struct {
			APIToken      string `json:"apiToken"`
			IdempotencyID string `json:"idempotencyId"`
		}
		if err := json.Unmarshal(payload, &body); err != nil || body.APIToken != "api_new" || body.IdempotencyID != "idem" {
			t.Fatalf("payload %s, %v", payload, err)
		}
		return Response{Status: http.StatusOK, Body: []byte(`{"agentId":"agm_1","workstreamCode":"348"}`)}, nil
	}
	response, err := Join(request, "device-token", "agm_1", "348", "api_new", "idem")
	if err != nil || response.Status != http.StatusOK || calls != 1 {
		t.Fatalf("response=%+v err=%v calls=%d", response, err, calls)
	}
}

func TestResolveOrganization(t *testing.T) {
	orgs := []Organization{{"org_a", "Team"}, {"org_b", "team"}}
	for _, tc := range []struct{ input, want, message string }{
		{"org_b", "org_b", ""}, {"Team", "org_a", ""}, {"TEAM", "", "More than one"}, {"Other", "", "No workspace"},
	} {
		got, err := ResolveOrganization(orgs, tc.input)
		if got != tc.want || (tc.message == "" && err != nil) || (tc.message != "" && (err == nil || !strings.Contains(err.Error(), tc.message))) {
			t.Fatalf("resolve %q: %q, %v", tc.input, got, err)
		}
	}
}
