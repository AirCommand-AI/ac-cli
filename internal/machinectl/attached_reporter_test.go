package machinectl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AirCommand-AI/ac-cli/internal/supervisor"
)

type attachedStatusSource struct{ *fakeStatusSource }

func (s *attachedStatusSource) List(context.Context) ([]supervisor.AgentStatus, error) {
	return []supervisor.AgentStatus{{AgentID: "agm_attached", Kind: "attached", Mode: "attached", State: "running"}}, nil
}
func TestMachineReporterIncludesAttachedKindAndMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Agents []struct{ Kind, Mode, AgentID string } `json:"agents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Agents) != 1 || body.Agents[0].Kind != "attached" || body.Agents[0].Mode != "attached" {
			t.Errorf("attached status missing: %+v", body)
		}
		_, _ = w.Write([]byte(`{"machine":{"state":"online"}}`))
	}))
	defer server.Close()
	r := HTTPReporter{URL: server.URL, Token: "device", Source: &attachedStatusSource{&fakeStatusSource{}}}
	if err := r.Report(context.Background()); err != nil {
		t.Fatal(err)
	}
}
