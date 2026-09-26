package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Structured tasks (ac-dashboard#24): number addressing, structured fields,
// cancellation, and the tasks columns and filters.

const structuredDetail = `{"workstream":{"code":"694"},"tasks":[` +
	`{"id":"aaaaaaaaaaaaaaa1","number":1,"status":"landed","assignee":"agent-7","title":"Schema","createdAt":"2026-09-08T10:00:00.000000000Z","updatedAt":"2026-09-08T10:00:00.000000000Z","milestone":"Phase 1","type":"Coding"},` +
	`{"id":"aaaaaaaaaaaaaaa5","number":5,"status":"in_flight","assignee":"agent-7","title":"API","description":"","createdAt":"2026-09-08T11:00:00.000000000Z","updatedAt":"2026-09-08T12:00:00.000000000Z",` +
	`"milestone":"Phase 2","type":"Coding","acceptance":["returns 200","documented"],"validation":"just test","dependsOn":["aaaaaaaaaaaaaaa1"],"links":["https://github.com/AirCommand-AI/ac-dashboard/issues/24"]},` +
	`{"id":"aaaaaaaaaaaaaaa6","number":6,"status":"cancelled","assignee":"","title":"Old API","createdAt":"2026-09-08T11:30:00.000000000Z","updatedAt":"2026-09-08T12:30:00.000000000Z",` +
	`"milestone":"Phase 2","cancelReason":"folded into #5","replacedBy":"aaaaaaaaaaaaaaa5","cancelledBy":{"nature":"human","id":"u1","name":"rahul@example.com"},"cancelledAt":"2026-09-08T12:30:00.000000000Z"},` +
	`{"id":"aaaaaaaaaaaaaaa7","number":7,"status":"archived","assignee":"","title":"Future status","createdAt":"2026-09-08T11:40:00.000000000Z","updatedAt":"2026-09-08T11:40:00.000000000Z"}` +
	`],"updates":[{"id":"u1","taskId":"aaaaaaaaaaaaaaa5","author":"old-name","authorActor":{"nature":"agent","id":"agent-9","name":"Pi"},"body":"started","createdAt":"2026-09-08T11:10:00.000000000Z"}]}`

// taskAPI is a fake agent API that serves structuredDetail and records every
// write, answering it with the task it names.
type taskAPI struct {
	mu       sync.Mutex
	requests []string
	bodies   []map[string]any
	reply    string
	status   int
}

func (f *taskAPI) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, request.Method+" "+request.URL.EscapedPath())
		if request.Method == http.MethodGet {
			_, _ = writer.Write([]byte(structuredDetail))
			return
		}
		raw, _ := io.ReadAll(request.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body %q: %v", raw, err)
		}
		f.bodies = append(f.bodies, body)
		if f.status != 0 {
			writer.WriteHeader(f.status)
		}
		_, _ = writer.Write([]byte(f.reply))
	})
}

func runTaskCommand(t *testing.T, api *taskAPI, arguments ...string) (int, string, string) {
	t.Helper()
	server := httptest.NewServer(api.handler(t))
	defer server.Close()
	client, stdout, stderr := testApp(t, server.URL, "", deterministicRandom(0x5a))
	saveTestCredential(t, client, testCredential())
	exitCode := client.Run(arguments)
	return exitCode, stdout.String(), stderr.String()
}

func TestParseTaskNumber(t *testing.T) {
	cases := []struct {
		ref  string
		want int
		ok   bool
	}{
		{"5", 5, true}, {"#5", 5, true}, {"0", 0, false}, {"05", 0, false}, {"task-1", 0, false},
		{"1234567890123456", 0, false}, {"aaaaaaaaaaaaaaa5", 0, false}, {"#", 0, false},
	}
	for _, tc := range cases {
		if got, ok := parseTaskNumber(tc.ref); got != tc.want || ok != tc.ok {
			t.Errorf("parseTaskNumber(%q) = %d, %t; want %d, %t", tc.ref, got, ok, tc.want, tc.ok)
		}
	}
}

func TestTaskShowsStructuredFieldsByNumber(t *testing.T) {
	for _, ref := range []string{"5", "#5", "aaaaaaaaaaaaaaa5"} {
		t.Run(ref, func(t *testing.T) {
			api := &taskAPI{}
			exitCode, stdout, stderr := runTaskCommand(t, api, "task", ref, "--workstream", "694")
			if exitCode != 0 {
				t.Fatalf("exit %d: %s", exitCode, stderr)
			}
			want := "Number: #5\n" +
				"Title: API\n" +
				"Description: -\n" +
				"Status: in_flight\n" +
				"Assignee: agent-7\n" +
				"Created: 2026-09-08T11:00:00.000000000Z\n" +
				"Updated: 2026-09-08T12:00:00.000000000Z\n" +
				"Milestone: Phase 2\n" +
				"Type: Coding\n" +
				"Acceptance:\n  - returns 200\n  - documented\n" +
				"Validation: just test\n" +
				"Depends on:\n  - #1 Schema (landed)\n" +
				"Links:\n  - https://github.com/AirCommand-AI/ac-dashboard/issues/24\n" +
				"Comments:\n" +
				"2026-09-08T11:10:00.000000000Z\tPi\tstarted\n"
			if stdout != want {
				t.Fatalf("stdout =\n%s\nwant\n%s", stdout, want)
			}
		})
	}

	t.Run("a cancelled task shows why and its replacement", func(t *testing.T) {
		exitCode, stdout, stderr := runTaskCommand(t, &taskAPI{}, "task", "6", "--workstream", "694")
		if exitCode != 0 {
			t.Fatalf("exit %d: %s", exitCode, stderr)
		}
		for _, line := range []string{"Status: cancelled\n", "Cancelled: folded into #5\n", "Cancelled by: rahul@example.com at 2026-09-08T12:30:00.000000000Z\n", "Replaced by: #5 API (in_flight)\n"} {
			if !strings.Contains(stdout, line) {
				t.Fatalf("stdout lacks %q:\n%s", line, stdout)
			}
		}
	})

	t.Run("an unknown number", func(t *testing.T) {
		exitCode, _, stderr := runTaskCommand(t, &taskAPI{}, "task", "9", "--workstream", "694")
		if exitCode == 0 || !strings.Contains(stderr, "Task 9 was not found in workstream 694.") {
			t.Fatalf("exit %d, stderr %q", exitCode, stderr)
		}
	})
}

const taskReplyFive = `{"id":"aaaaaaaaaaaaaaa5","number":5,"status":"%s","title":"API","createdAt":"2026-09-08T11:00:00.000000000Z","updatedAt":"2026-09-08T13:00:00.000000000Z"}`

func TestTaskChangesAddressedByNumber(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		reply string
		want  map[string]any
	}{
		{"status", []string{"--status", "landed"}, strings.Replace(taskReplyFive, "%s", "landed", 1),
			map[string]any{"status": "landed"}},
		{"cancel with replacement", []string{"--status", "cancelled", "--reason", "superseded", "--replaced-by", "#1"}, strings.Replace(taskReplyFive, "%s", "cancelled", 1),
			map[string]any{"status": "cancelled", "cancelReason": "superseded", "replacedBy": "#1"}},
		{"structured edit", []string{"--milestone", "Phase 3", "--acceptance", "a", "--acceptance", "b", "--link", "https://x.example/1", "--depends-on", ""}, strings.Replace(taskReplyFive, "%s", "in_flight", 1),
			map[string]any{"milestone": "Phase 3", "acceptance": []any{"a", "b"}, "links": []any{"https://x.example/1"}, "dependsOn": []any{}}},
		{"clearing a label", []string{"--type", ""}, strings.Replace(taskReplyFive, "%s", "in_flight", 1),
			map[string]any{"type": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &taskAPI{reply: tc.reply}
			exitCode, stdout, stderr := runTaskCommand(t, api, append([]string{"task", "5", "--workstream", "694"}, tc.args...)...)
			if exitCode != 0 {
				t.Fatalf("exit %d: %s", exitCode, stderr)
			}
			if want := []string{"GET /agent/v1/workstreams/694", "PATCH /agent/v1/workstreams/694/tasks/aaaaaaaaaaaaaaa5"}; strings.Join(api.requests, "|") != strings.Join(want, "|") {
				t.Fatalf("requests = %v, want %v", api.requests, want)
			}
			body := api.bodies[0]
			if key, _ := body["idempotencyId"].(string); key == "" {
				t.Fatalf("patch without an idempotency key: %v", body)
			}
			delete(body, "idempotencyId")
			if got, _ := json.Marshal(body); string(got) != mustJSON(t, tc.want) {
				t.Fatalf("patch body = %s, want %s", got, mustJSON(t, tc.want))
			}
			if !strings.HasPrefix(stdout, "Number: #5\nTitle: API\n") {
				t.Fatalf("stdout = %q", stdout)
			}
		})
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestTaskChangeRulesBeforeAnyRequest(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"cancel without a reason", []string{"--status", "cancelled"}, "--status cancelled requires --reason <text>."},
		{"cancel with a blank reason", []string{"--status", "cancelled", "--reason", " "}, "--status cancelled requires --reason <text>."},
		{"a reason without cancelling", []string{"--status", "landed", "--reason", "x"}, "--reason and --replaced-by go only with --status cancelled."},
		{"a replacement without cancelling", []string{"--replaced-by", "1"}, "--reason and --replaced-by go only with --status cancelled."},
		{"an edit with a status", []string{"--status", "landed", "--milestone", "x"}, "cannot be combined with --status, --comment or --assignee"},
		{"an edit with a comment", []string{"--comment", "hi", "--type", "x"}, "cannot be combined with --status, --comment or --assignee"},
		{"a number cannot be edited", []string{"--number", "3"}, taskByIDUsage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &taskAPI{}
			exitCode, _, stderr := runTaskCommand(t, api, append([]string{"task", "5", "--workstream", "694"}, tc.args...)...)
			if exitCode == 0 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit %d, stderr %q, want %q", exitCode, stderr, tc.want)
			}
			if len(api.requests) != 0 {
				t.Fatalf("requests = %v, want none", api.requests)
			}
		})
	}
}

func TestTaskFieldRefusalsAreExplained(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{"TaskDependencyCycle", "Those dependencies would form a loop."},
		{"TaskReferenceNotFound", "A task reference does not match a task in this workstream: no task #9"},
		{"TaskReopenForbidden", "Only an agent can reopen a cancelled task."},
		{"InvalidTaskField", "AirCommand rejected a task field: no task #9"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			api := &taskAPI{status: http.StatusUnprocessableEntity, reply: `{"message":"no task #9","code":"` + tc.code + `"}`}
			exitCode, _, stderr := runTaskCommand(t, api, "task", "5", "--workstream", "694", "--depends-on", "9")
			if exitCode == 0 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit %d, stderr %q, want %q", exitCode, stderr, tc.want)
			}
		})
	}
}

func TestTaskCreateSendsStructuredFields(t *testing.T) {
	created := `{"id":"aaaaaaaaaaaaaaa8","number":8,"status":"todo","title":"New","createdAt":"2026-09-08T13:00:00.000000000Z","updatedAt":"2026-09-08T13:00:00.000000000Z"%s}`

	t.Run("every field, no warnings", func(t *testing.T) {
		api := &taskAPI{status: http.StatusCreated, reply: strings.Replace(created, "%s", `,"acceptance":["a"],"validation":"v"`, 1)}
		exitCode, stdout, stderr := runTaskCommand(t, api, "task", "create", "--workstream", "694", "--title", "New", "--number", "8",
			"--milestone", "Phase 2", "--type", "Coding", "--acceptance", "a", "--validation", "v", "--depends-on", "1", "--depends-on", "#5", "--link", "https://x.example/1")
		if exitCode != 0 {
			t.Fatalf("exit %d: %s", exitCode, stderr)
		}
		if stdout != "Created task: aaaaaaaaaaaaaaa8\nNumber: #8\n" || stderr != "" {
			t.Fatalf("stdout %q, stderr %q", stdout, stderr)
		}
		body := api.bodies[0]
		delete(body, "idempotencyId")
		want := map[string]any{"title": "New", "description": "", "assignee": "", "status": "todo", "number": float64(8), "milestone": "Phase 2", "type": "Coding",
			"acceptance": []any{"a"}, "validation": "v", "dependsOn": []any{"1", "#5"}, "links": []any{"https://x.example/1"}}
		if mustJSON(t, body) != mustJSON(t, want) {
			t.Fatalf("create body = %s, want %s", mustJSON(t, body), mustJSON(t, want))
		}
	})

	t.Run("missing acceptance and validation warn", func(t *testing.T) {
		api := &taskAPI{status: http.StatusCreated, reply: strings.Replace(created, "%s", "", 1)}
		exitCode, stdout, stderr := runTaskCommand(t, api, "task", "create", "--workstream", "694", "--title", "New")
		if exitCode != 0 {
			t.Fatalf("exit %d: %s", exitCode, stderr)
		}
		wantErr := "Warning: the task has no acceptance criteria; add them with: aircom task 8 --workstream 694 --acceptance <text>\n" +
			"Warning: the task has no validation; add it with: aircom task 8 --workstream 694 --validation <text>\n"
		if stdout != "Created task: aaaaaaaaaaaaaaa8\nNumber: #8\n" || stderr != wantErr {
			t.Fatalf("stdout %q, stderr %q", stdout, stderr)
		}
		if _, sent := api.bodies[0]["number"]; sent {
			t.Fatalf("a number was sent without --number: %v", api.bodies[0])
		}
	})

	for _, args := range [][]string{{"--number", "0"}, {"--number", "1000000000"}, {"--status", "cancelled"}} {
		t.Run("refused "+strings.Join(args, " "), func(t *testing.T) {
			api := &taskAPI{}
			exitCode, _, _ := runTaskCommand(t, api, append([]string{"task", "create", "--workstream", "694", "--title", "New"}, args...)...)
			if exitCode == 0 || len(api.requests) != 0 {
				t.Fatalf("exit %d, requests %v", exitCode, api.requests)
			}
		})
	}

	t.Run("a taken number", func(t *testing.T) {
		api := &taskAPI{status: http.StatusConflict, reply: `{"message":"task number already in use","code":"TaskNumberTaken"}`}
		exitCode, _, stderr := runTaskCommand(t, api, "task", "create", "--workstream", "694", "--title", "New", "--number", "5")
		if exitCode == 0 || !strings.Contains(stderr, "That task number is already in use in this workstream.") {
			t.Fatalf("exit %d, stderr %q", exitCode, stderr)
		}
	})
}

func TestTasksShowsNumbersAndFiltersByMilestoneAndType(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"all, including cancelled and unknown statuses", nil,
			"#1\taaaaaaaaaaaaaaa1\tlanded\tagent-7\tPhase 1\tCoding\tSchema\n" +
				"#5\taaaaaaaaaaaaaaa5\tin_flight\tagent-7\tPhase 2\tCoding\tAPI\n" +
				"#6\taaaaaaaaaaaaaaa6\tcancelled\t-\tPhase 2\tOther\tOld API\n" +
				"#7\taaaaaaaaaaaaaaa7\tarchived\t-\t-\tOther\tFuture status\n"},
		{"milestone, any case", []string{"--milestone", "phase 2"},
			"#5\taaaaaaaaaaaaaaa5\tin_flight\tagent-7\tPhase 2\tCoding\tAPI\n" +
				"#6\taaaaaaaaaaaaaaa6\tcancelled\t-\tPhase 2\tOther\tOld API\n"},
		{"type Other matches no type", []string{"--type", "Other"},
			"#6\taaaaaaaaaaaaaaa6\tcancelled\t-\tPhase 2\tOther\tOld API\n" +
				"#7\taaaaaaaaaaaaaaa7\tarchived\t-\t-\tOther\tFuture status\n"},
		{"cancelled", []string{"--status", "cancelled"},
			"#6\taaaaaaaaaaaaaaa6\tcancelled\t-\tPhase 2\tOther\tOld API\n"},
		{"nothing matches", []string{"--milestone", "Phase 9"},
			"No tasks match those filters in workstream 694.\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exitCode, stdout, stderr := runTaskCommand(t, &taskAPI{}, append([]string{"tasks", "--workstream", "694"}, tc.args...)...)
			if exitCode != 0 || stdout != tc.want {
				t.Fatalf("exit %d, stderr %q, stdout =\n%s\nwant\n%s", exitCode, stderr, stdout, tc.want)
			}
		})
	}
}
