package app

import (
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	machineMetricsUsage = "Usage: aircom machine metrics --workstream <code> [--agent <id>] [--run <runId>] [--since <2h|RFC3339>] [--until <RFC3339>] [--limit N] [--cursor <cursor>]"
	maxMetricsPage      = 1440
)

// machineMetrics prints the run machine's health history (disk, memory, swap,
// CPU, load, agent dir sizes) as JSON, newest first. Without --run it uses the
// workstream's one running machine.
func (a *App) machineMetrics(args []string) error {
	flags := flag.NewFlagSet("machine metrics", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var code, agent, run, since, until, cursor string
	var limit int
	flags.StringVar(&code, "workstream", "", "workstream code")
	flags.StringVar(&agent, "agent", "", "agent ID or name")
	flags.StringVar(&run, "run", "", "run ID")
	flags.StringVar(&since, "since", "", "duration back from now (2h) or RFC 3339 time")
	flags.StringVar(&until, "until", "", "RFC 3339 time")
	flags.IntVar(&limit, "limit", 0, "samples per page")
	flags.StringVar(&cursor, "cursor", "", "cursor from the previous page")
	if flags.Parse(args) != nil || flags.NArg() != 0 || code == "" {
		return &publicError{message: machineMetricsUsage}
	}
	if err := validateWorkstreamCode(code); err != nil {
		return err
	}
	query, err := metricsQuery(since, until, limit, cursor, time.Now().UTC())
	if err != nil {
		return err
	}
	if run == "" {
		if run, err = a.activeRun(code, agent); err != nil {
			return err
		}
	}
	if !machineRunID.MatchString(run) {
		return &publicError{message: "--run must be a run ID like run_0123456789abcdef01234567."}
	}
	path := "/agent/v1/workstreams/" + url.PathEscape(code) + "/machine-runs/" + run + "/metrics"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return a.machineRead(code, agent, path, func(body []byte) bool {
		var page struct {
			Points []json.RawMessage `json:"points"`
		}
		return json.Unmarshal(body, &page) == nil && page.Points != nil
	})
}

func metricsQuery(since, until string, limit int, cursor string, now time.Time) (url.Values, error) {
	query := url.Values{}
	if since = strings.TrimSpace(since); since != "" {
		if d, err := time.ParseDuration(since); err == nil && d > 0 {
			query.Set("since", now.Add(-d).Format(time.RFC3339))
		} else if _, err := time.Parse(time.RFC3339, since); err == nil {
			query.Set("since", since)
		} else {
			return nil, &publicError{message: "--since must be a duration like 2h or an RFC 3339 time."}
		}
	}
	if until = strings.TrimSpace(until); until != "" {
		if _, err := time.Parse(time.RFC3339, until); err != nil {
			return nil, &publicError{message: "--until must be an RFC 3339 time."}
		}
		query.Set("until", until)
	}
	if limit != 0 {
		if limit < 1 || limit > maxMetricsPage {
			return nil, &publicError{message: "--limit must be an integer from 1 to 1440."}
		}
		query.Set("limit", strconv.Itoa(limit))
	}
	if cursor = strings.TrimSpace(cursor); cursor != "" {
		query.Set("cursor", cursor)
	}
	return query, nil
}

// activeRun returns the workstream's single running machine.
func (a *App) activeRun(code, agent string) (string, error) {
	credential, err := a.credentialFor(code, agent)
	if err != nil {
		return "", err
	}
	response, err := a.request(http.MethodGet, "/agent/v1/workstreams/"+url.PathEscape(code)+"/machine-runs?state=active&limit=2", credential.APIToken, nil)
	if err != nil {
		return "", err
	}
	if response.status < 200 || response.status >= 300 {
		return "", workstreamResponseStatusError(response.status, response.body, code, false, credential)
	}
	var page struct {
		Runs []struct {
			RunID string `json:"runId"`
		} `json:"runs"`
	}
	if json.Unmarshal(response.body, &page) != nil {
		return "", &publicError{message: "The workstream service returned an invalid machine run response."}
	}
	switch len(page.Runs) {
	case 0:
		return "", &publicError{message: "No running machine in this workstream; pass --run <runId>."}
	case 1:
		return page.Runs[0].RunID, nil
	}
	return "", &publicError{message: "More than one running machine; pass --run <runId> (see aircom machine list --state active)."}
}
