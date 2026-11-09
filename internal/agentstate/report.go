package agentstate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"
)

// Reporter sends K1 state using the agent credential. It is independent of
// the supervisor, so integration can inject a fake HTTP server in tests.
type Reporter struct {
	BaseURL string
	Client  *http.Client
	Token   string
}

func (r Reporter) Report(ctx context.Context, workstream string, state State, at time.Time) error {
	if r.Client == nil || r.Token == "" {
		return fmt.Errorf("state API is not configured")
	}
	if state.Physical != Running && state.Physical != Stopped {
		return fmt.Errorf("invalid physical state")
	}
	if state.Physical == Running {
		switch state.Logical {
		case Working, Idle, Waiting, Stuck, NoActivity, Unknown:
		default:
			return fmt.Errorf("invalid logical state")
		}
	} else if state.Logical != "" {
		return fmt.Errorf("stopped state must omit logical")
	}
	if !ValidReason(state.Reason) || state.Since.IsZero() || at.IsZero() || workstream == "" {
		return fmt.Errorf("invalid state report")
	}
	base, err := url.Parse(r.BaseURL)
	if err != nil || base == nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return fmt.Errorf("invalid state API URL")
	}
	path := "/agent/v1/workstreams/" + workstream + "/agents/me/state"
	target := base.ResolveReference(&url.URL{Path: path, RawPath: "/agent/v1/workstreams/" + url.PathEscape(workstream) + "/agents/me/state"})
	type tool struct {
		Name    string `json:"name"`
		Command string `json:"command,omitempty"`
		Since   string `json:"since,omitempty"`
	}
	body := struct {
		Physical Physical `json:"physical"`
		Logical  Logical  `json:"logical,omitempty"`
		Reason   string   `json:"reason,omitempty"`
		Since    string   `json:"since"`
		Source   string   `json:"source"`
		At       string   `json:"at"`
		Tool     *tool    `json:"tool,omitempty"`
	}{Physical: state.Physical, Logical: state.Logical, Reason: state.Reason, Since: state.Since.UTC().Format(time.RFC3339Nano), Source: "daemon", At: at.UTC().Format(time.RFC3339Nano)}
	if state.Tool.Name != "" {
		body.Tool = &tool{Name: state.Tool.Name, Command: state.Tool.Command}
		if !state.Tool.Since.IsZero() {
			body.Tool.Since = state.Tool.Since.UTC().Format(time.RFC3339Nano)
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target.String(), bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := r.Client.Do(req)
	if err != nil {
		return fmt.Errorf("state report: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("state API returned HTTP %d", res.StatusCode)
	}
	return nil
}

// ValidReason can be used by integrations before storing a proposed reason.
func ValidReason(reason string) bool {
	return utf8.ValidString(reason) && utf8.RuneCountInString(reason) <= 120
}
