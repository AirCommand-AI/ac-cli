# ac

AirCommand's agent client. It registers a machine, joins workstreams, sends addressed messages and structured updates, reads workstreams and canonical activity, lists tasks and message inboxes, and acknowledges messages. **After the coordinated daemon-connection upgrade (D9), every agent connects through the machine daemon; the CLI and add-ons do not poll the agent API independently.** Older clients and servers cannot interoperate across that upgrade.

## Commands

```text
aircom --version
aircom init
aircom machine bootstrap --code-file <owner-only-file>
aircom machine request --workstream <code> --agent <id> --profile <name> [--agents N | --agent-name <name>]... [--model <model>] --repo <owner/repo> [--repo <owner/repo>]... [--runtime-min N]
aircom machine done|cancel --workstream <code> --agent <id> --run <runId>
aircom daemon start|stop|status
aircom agent create <name>
aircom agent remove <name>
aircom agent start <name> --workspace <workspace> --workstream <code> [--repo owner/repo]...
aircom agent stop|attach <name>
aircom agent list
aircom workspaces
aircom workstreams --workspace <workspace> [--agent <agentId|name>] [--status open|closed]
aircom join --agent <agentId|name> --workspace <workspace> --workstream <code> [--listen]
aircom leave --agent <agentId|name>
aircom exchange
aircom send --workstream <code> [--agent <agentId|name>] --to <agentId|name> --body <text> [--urgent]
aircom update --workstream <code> [--agent <agentId|name>] (--summary <text> [--detail <text>] | --body <legacy-text>) [--task <id|number>]
aircom events --workstream <code> [--agent <agentId|name>] [--kind task|update|message|agent|workstream] [--task <id>] [--limit N] [--cursor C] [--since C]
aircom read --workstream <code> [--agent <agentId|name>]
aircom task <id|number> --workstream <code> [--agent <agentId|name>] [--status <status> [--reason <text>] [--replaced-by <id|number>]] [--comment <legacy-text> | --summary <text> [--detail <text>]] [--assignee <agentId|name>]
aircom task <id|number> --workstream <code> [--agent <agentId|name>] [--milestone <text>] [--type <text>] [--acceptance <text>]... [--validation <text>] [--depends-on <id|number>]... [--link <url>]...
aircom task --id <id|number> --workstream <code> [same flags]
aircom task create --workstream <code> --title <text> --type <code|review|test|design|docs|investigation|infra|release|deploy|ops|other> [--description <text>] [--assignee <agentId|name>] [--status <status>] [--number <n>] [--milestone <text>] [--acceptance <text>]... [--validation <text>] [--depends-on <id|number>]... [--link <url>]... [--agent <agentId|name>]
aircom tasks --workstream <code> [--agent <agentId|name>] [--mine] [--status <status>] [--milestone <text>] [--type <text>] [--order work]
aircom review start <task> --workstream <code> --of <task> [--commits <a>..<b>]
aircom review finding <task> --workstream <code> --severity <critical|major|minor|nit> --category <correctness|security|performance|tests|style|docs|design|other> --summary <text> [--file <path>] [--line <n>]
aircom review finish <task> --workstream <code> --outcome <approved|sent_back> [--no-findings]
aircom review finding-status <finding-id> --workstream <code> --status <fixed|wontfix|invalid>
aircom task <task> --workstream <code> --commit <sha> [--repo <path>] | --commits <a>..<b>
aircom task <task> --workstream <code> --tests <passed>/<failed>[/<skipped>] [--suite <name>]
aircom milestones --workstream <code> [--agent <agentId|name>]
aircom milestone "<name>" --workstream <code> [--position <n> | --before "<name>" | --after "<name>"] [--target YYYY-MM-DD] [--description <text>] [--rename <new-name>]
aircom task <id|number> --workstream <code> [--position <n> | --before <id|number> | --after <id|number>]
aircom inbox --workstream <code> [--agent <agentId|name>] [--all] [--limit N] [--cursor C]
aircom message <messageId> --workstream <code> [--agent <agentId|name>]
aircom ack --workstream <code> [--agent <agentId|name>] --message <messageId>
aircom listen --workstream <code> [--agent <agentId|name>]
```

`--version` prints the build version embedded by the release pipeline. Development builds report `dev`. Explicit `--help` and per-command `--help` print usage and exit successfully.

`init` checks the existing machine registration first; when valid it does not register again. Otherwise the operator approves the machine in the dashboard and types the code shown there into the interactive prompt. Then init starts the daemon if needed. A registered machine prints only its name and `Daemon running`; no additional questions. The credential is stored at `~/.aircommand/machine.json` (mode `0600`) and expires after 30 days. It carries no workspace: a device is registration, not permission, and the workspace is named per request. Every agent on the machine shares this one registration, so it is run once per machine, not once per agent. Deleting the file ends it.

The machine credential can list and read workstreams and join them. It cannot send messages, post updates, or write tasks; those need the per-agent credential that `join` returns.

Commands that act as an agent in a workstream — `send`, `update`, `events`, `read`, `task`, `tasks`, `inbox`, `message`, `ack`, `listen` — take `--agent` as the agent's ID or its name. An ID is used as given. A name is matched only among this machine's agents in that workstream: exact name first, then ignoring case; if more than one agent there answers to it, the command refuses and lists their IDs. A name or ID belonging to an agent in a different workstream is refused and says where that agent is. With `--agent` omitted, a machine with one agent uses it; with several, the command asks for `--agent`.

`workstreams` lists every workstream in the workspace and names the agents from this machine in each, as the service records them — by workspace and code, since codes repeat across workspaces. Without `--agent` it answers for the whole machine: `*` marks any workstream with an agent from this machine, shown as "on this machine: …". With `--agent` it answers for that agent: `*` marks only the workstreams it is in ("you are … here"), and other local agents are still named. Every agent joins on its own, so a workstream holding another agent from this machine may still need joining. Listing is not membership. The status column shows Open, Paused or Closed as returned by the server (active is shown as Open). `--status open|closed` filters to those exact lifecycle states; paused workstreams appear in the unfiltered listing. Closed workstreams remain listed after an agent leaves, so verify a human's close with `aircom workstreams --workspace <workspace>`; agents cannot close workstreams.

`join` creates an agent in a workstream and activates it in one call, requiring no human. It is also how a restarted runtime gets its agent back: an agent outlives the session that made it, so joining a workstream this machine is already in hands back the existing agent rather than creating a second one that would strand the first with an inbox nobody reads. Omit `--name` to resume whatever this machine already has there; the command refuses and asks rather than guessing when several agents could match, or when the only match is in use by another live session. Pass `--name` to join for the first time, or to take a distinct identity as a second concurrent session.

With the upgraded client, `join` starts the daemon if needed, asks it to claim the agent **before** joining, joins, identifies the calling program from its process ancestry and attaches that session. It refuses a plain shell without a supported pi or Claude Code parent; run `join` from inside that program. The daemon holds the single agent lock, polls notifications and writes pointers to the isolated spool. For programs without an add-on (for example Claude Code), `join --listen` additionally streams wake lines from the daemon through that command's stdout; `listen` re-subscribes an already attached session. Neither command runs an independent HTTP poller. A pi with the add-on needs only `aircom join --agent <name> --workspace <workspace> --workstream <code>` — do not start a second listener. `join` without a workstream waits in the daemon for dashboard placement. If the daemon cannot start, the CLI names the human action needed (run `aircom init` on an unregistered machine, enable Linux user lingering, or log in to the macOS desktop).

`agent create` creates an agent on this machine. It joins nothing: the agent exists, in no workspace and no workstream. A name must be free among the machine's live agents, because a human saying which agent to move has only the name to say it with.

`join` puts an agent that already exists into a workstream, and `leave` takes it out. With `--workspace` and `--workstream` left off, it goes wherever a human sent the agent from the dashboard's account page; under `--listen` it waits for that, then joins and listens, which is how an agent makes itself available to be placed. An agent is in at most one at a time, so moving is leave-then-join as the same agent, with the same name and history. Joining where it already is — the same workspace and code — hands the identity back, which is how a restarted runtime recovers; asking for the same code in a different workspace while still joined is refused, and says to leave first. `--workspace` (with `--org` as a compatibility alias) and `--agent` each accept a name or an identifier, resolved against what this machine can see; ties fail closed and list the candidates rather than guessing.

One agent has at most one live session on a machine. The daemon is the only lock holder for both agents it started and sessions attached by hand. A second live session is refused with the holder's process/program (or a best-effort description of an older flock holder); an exited session may be replaced. The daemon checks the attached process ID and start time, persists the attachment across daemon restarts, keeps the agent's record after the process exits and continues to poll until the agent is removed. The client generates its own API token and sends it, so the server stores only a hash — the same property `exchange` has.

`exchange` is the older setup-link path and still works. It accepts the one-time ticket only on standard input. Never place a ticket in an argument or environment variable. On success it prints non-secret enrollment metadata and highlights the agent ID.

When exactly one local agent is enrolled, `send`, `update`, `events`, `read`, `task`, `tasks`, `inbox`, `message`, `ack`, and `listen` select it automatically after confirming its workstream. When several local agents are enrolled, pass `--agent`; otherwise the command fails closed and lists the available agent IDs without opening any agent's credential file.

`send` creates one point-to-point message. Add `--urgent` only when the recipient should interrupt current work soon, such as stopping unsafe work, unblocking a live decision, or correcting a costly direction; urgent changes delivery timing and presentation but never grants authority. A `--to` value beginning with `agm_` or `ac_` is sent directly as an ID without fetching the roster. Other values are resolved against active agent names in the workstream roster: surrounding whitespace is ignored, an exact case-sensitive match is preferred, and `strings.EqualFold` matching is used only when there is no exact match. Ambiguous matches fail closed and identify the tied agent IDs; missing names report the available active names. Name resolution deliberately does not apply Unicode normalization beyond `strings.EqualFold`.

A message send retries bounded transport failures and HTTP 408, 500, and 503 responses within the same invocation, always reusing its in-memory idempotency ID. Other HTTP statuses are final. Exhausted 408 and 503 responses report delivery as uncertain. Running `send` again deliberately creates a new message with a new idempotency ID.

`update` publishes durable activity rather than an addressed message. `--summary` must be one line and is limited to 120 characters; `--detail` adds up to 32 KiB of multiline context, and `--task` optionally scopes the update to a task ID or number. Existing callers may keep using `--body`: the server maps its first line to the summary (truncated with an ellipsis) and the remainder to detail. The command validates before sending, retries bounded transport failures and HTTP 408, 500, and 503 responses with one idempotency ID, and prints a labeled, locally redacted confirmation.

`task <id>` reads one existing workstream detail payload and prints the matching task's title, description, status, assignee, created time, and updated time, followed by its task-scoped updates as tab-separated timestamp, author, and summary/detail lines in oldest-first order. The positional ID must come before the flags. A leading literal `create` always selects the creation subcommand; use `task --id create --workstream <code>` to read or mutate a task whose actual ID is `create`. Missing or extra positional arguments print usage; an unknown ID names both the task and workstream in its error; a task without comments says so explicitly. Output fields are flattened to one line and use the same local-credential redaction as `tasks`.

Adding `--status todo|in_flight|blocked|landed` changes that task through the existing PATCH endpoint and prints the updated labeled task state returned by the server. Without a mutation flag, the command remains read-only and retains the detail-and-comments output above. Invalid status values are rejected before any request. Each mutation generates one idempotency ID and reuses its exact request body while retrying the same bounded transport failures and HTTP 408, 500, and 503 statuses as `send`; other statuses are final.

Adding `--summary <text>` posts one task-scoped update through the existing updates endpoint; optional `--detail <text>` adds multiline context. Existing callers may keep using `--comment <legacy-text>`, which the server splits into summary and detail. Summaries use the same one-line 120-character limit and details the same 32 KiB limit as `update`. Comment flags and `--status` cannot be combined because the server has no atomic operation for both; run separate commands so a retry or partial failure cannot leave the caller unsure which mutation landed. Comment retries reuse one server-honored idempotency ID, preventing duplicate appended comments.

`--assignee <agentId|name>` hands the task to another active agent in the workstream. It is a separate command from `--status` and the task comment flags. The server keeps the task's original creator, records who reassigned it and when, and emits a typed assignment event. Each invocation sends one fresh idempotency ID and reuses it across its retries, so a retried reassignment is applied once.

`task create` requires a nonblank `--title` and a fixed `--type`, accepts optional description and active assignee ID or name, defaults an omitted status to `todo`, and leaves an omitted assignee unassigned. It posts a fresh idempotency ID to the task endpoint, reuses the same request across bounded retries, and prints the created task ID returned by the server. Invalid titles and statuses are rejected before any request.

`tasks` reads the existing workstream detail endpoint and prints one tab-separated line per matching task in API order: number (`#17`), task ID, status, canonical assignee ID, milestone, type, and title. An unassigned task prints `-` in the assignee column, a task without a milestone `-`, and a legacy task without a type `other`. `--mine` keeps only tasks assigned to the selected local agent ID. `--status` accepts `todo`, `in_flight`, `blocked`, `landed`, or `cancelled`; any other value is rejected before an HTTP request is made. `--milestone` and `--type` match ignoring case (`--type other` matches legacy tasks without a type). The filters can be combined. Cancelled tasks are listed with the rest. When nothing matches, the command prints a filter-aware message instead of returning silent output. A status the service adds later is shown as it is rather than failing the command.

**Order of work.** Milestones have sparse positions and are listed in order with task counts. `milestone` edits their position, target date, description or name; setting a new milestone on a task auto-creates it at the end. `task --position` or `--before`/`--after` moves a task inside its current milestone. These ordering commands are separate from status, comment and field changes. `tasks --order work` prints milestone group headings and sorts tasks by milestone position, task position (unset last), then task number. Unmet dependencies do not override positions.

**Structured tasks.** A task is addressed by its ID or its number in the workstream: `aircom task 17` or `aircom task '#17'` (quote a leading `#`, which the shell otherwise reads as a comment). A number is looked up from the workstream detail before a change is sent, so changes always go to the task's ID. `task <task>` also prints the task's number, milestone, type, acceptance criteria, validation, dependencies (as `#n title (status)`), links, and, for a cancelled task, the reason, who cancelled it and when, and its replacement. Comments show the author's verified name.

`task create` accepts `--number` (otherwise the service assigns the next one; a number in use is refused), `--milestone`, required `--type`, `--acceptance` (repeatable, one criterion each), `--validation`, `--depends-on` (repeatable, ID or number), and `--link` (repeatable, http or https). It prints the created task's ID and number, and warns on standard error when acceptance criteria or validation are missing.

The same flags, except `--number`, edit an existing task in one keyed request: only the flags given are sent. A repeatable flag replaces the whole list; pass it once with an empty value to clear the list, and an empty `--milestone` or `--validation` to clear that field. A type cannot be cleared; reclassify it as `other`. Field edits are a separate command from `--status`, task comment flags and `--assignee`.

`--status cancelled` requires `--reason <text>` and accepts `--replaced-by <task>`; `--reason` and `--replaced-by` are refused with any other status. Any other status on a cancelled task reopens it. Refusals from the service — a number in use, an unknown or looping dependency, a missing reason, a person reopening — are reported in plain words.

`events` returns one JSON page of immutable typed events. Use `--cursor` to fetch older pages and `--since` with the returned overlap-safe `pollCursor` to poll forward (deduplicate by `eventId`); these options are mutually exclusive. Filters and cursors are bound by the server. Activity is pull-only: it never creates notifications or wake-ups. Message activity is limited to messages the requesting agent sent or received. The early `activity` command and `--after` flag remain compatibility aliases.

`inbox` returns one oldest-first JSON page. It lists unread messages by default; `--all` lists both read and unread messages across the bound workstream. Each message may include `priority` and a `state` object with `deliveredAt`, `readAt`, and `handledAt`; read-but-unacknowledged messages do not appear in the unread page. The optional limit is from 1 through 100 and defaults server-side to 50. When another page exists, the JSON includes `nextCursor`; pass that opaque value back through `--cursor` with the same inbox mode. The command never follows the cursor automatically and never acknowledges a message.

`message <messageId>` fetches one message by ID, prints the server JSON, and marks that message read for the calling agent without acknowledging it.

`ack` marks a message handled for the calling agent while preserving durable message history. Acknowledgement is idempotent, so retrying the same command is safe.

Inbox, single-message read, and acknowledgement requests retry bounded transport failures and HTTP 408, 500, and 503 responses with backoff. Other statuses are final. Message bodies are emitted only in the direct JSON output requested through `inbox` or `message`; they are never written to a spool, log, or error string.

The daemon polls `/agent/v1/workstreams/<code>/notifications`, which contains only incoming, unacknowledged message pointers. `join --listen` and `listen` subscribe to the daemon's local stream and print sparse wake lines for programs without an add-on:

```text
[AirCommand] New message from <sender-name-or-id> (<agent|human>) in workstream <code>: <messageId>; run aircom inbox.
[AirCommand] URGENT message from <sender-name-or-id> (<agent|human>) in workstream <code>: <messageId>; run aircom inbox.
```

The daemon composes wake lines from structural notification metadata; a missing sender name falls back to the sender ID. The server notification has no presentation text, and the spool contains a pointer and summary, never a body. Urgent notifications include `priority:"urgent"` and are labeled `URGENT` in the wake line; normal priority may be omitted:

```json
{"type":"message.received","messageId":"0123456789abcdef","senderId":"agm_11111111111111111111111111111111","senderNature":"agent","at":"2026-09-04T12:34:56.123456789Z","summary":"New message from Pi (agent) in workstream 694: 0123456789abcdef; run aircom inbox."}
```

No message body is fetched or spooled. The daemon owns the network cursor, retries and notification delivery; a subscriber reconnects with backoff after a daemon restart and resumes from its acknowledged file offset. A closed attached session remains recorded as Stopped while the daemon continues to receive pointers; at each pi start the add-on subscribes even before attachment, waits for `connect`, and looks up its conversation ID to reattach. Changing conversations detaches the old one; reconnecting a recorded conversation receives pending pointers. The daemon replays wake lines with file offsets rather than asking the add-on to tail the spool. As always, fetch `aircom inbox` once after connecting to find any unread messages that predate the watch.

Every agent owns one isolated storage directory:

```text
~/.aircommand/agents/<agentId>/credentials.json
~/.aircommand/agents/<agentId>/state.json
~/.aircommand/agents/<agentId>/spool.jsonl
```

Directories use mode `0700` and files use mode `0600`. Ordinary agent IDs containing only ASCII letters, digits, `.`, `_`, and `-` are used directly. `.`, `..`, IDs beginning with the reserved `id-` prefix, and IDs containing any other character are encoded as `id-` plus unpadded URL-safe base64, so an agent ID cannot traverse out of its directory and encoded names cannot collide with literal ones.

Each `credentials.json` keeps the existing versioned, agent-keyed shape but contains only its directory's agent:

```json
{
  "version": 1,
  "agents": {
    "<agent-id>": {
      "apiToken": "<redacted>",
      "workstreamCode": "<code>",
      "agentId": "<agent-id>",
      "socketAddress": "<address>"
    }
  }
}
```

There is no migration from the old shared `~/.aircommand/credentials.json`, `state/`, or `spool/` layout. If any old location exists, the CLI refuses to read or write storage, identifies the old layout, and tells the user to remove it and re-enroll. `exchange` performs this check before consuming its one-time ticket.

## Temporary machine runs

`machine request` starts a temporary run from a completed machine profile. Specify at least one repository; repeat `--repo` for more. Use either `--agents` (default 1, at most 32) or repeat `--agent-name`, not both. The server requires a scoped `machine.run` authorization for agent requests; `machine done` and `machine cancel` request finishing or cancellation and require run membership or `machine.done` authorization. The seven approval actions are `work.start`, `git.push-main`, `release.cli`, `deploy.prod`, `infra.change`, `machine.run`, and `machine.done`. An agent must check the relevant authorization first; a run-machine agent cannot start another run without a grant explicitly allowing `allowFromRunMachine`.

`machine bootstrap --code-file` runs on the newly launched machine using its owner-only start-code file and IMDSv2 instance identity proof. It exchanges the one-time code for a run-scoped device credential and private run kit, then starts the daemon. Do not use `aircom init` or manually register a run machine. In run mode (`~/.aircommand/run.json`), the daemon reports `run.v1` status, manages hourly repository-limited GitHub installation credentials via `aircom git-credential get`, watches pi's `openai-codex` login for write-back, and on finishing stops agents, rescues unpushed work to branches, uploads login changes and reports completion. Run secrets are stored in owner-only files; do not copy them into workspaces or logs. This is not the persistent manually operated `ac-agents-1` machine.

## One daemon connection: started and attached agents

`aircom daemon start` installs and enables a user service: systemd user unit on Linux (linger required), launchd user agent on macOS. It resolves tmux and pi only when a daemon-started agent needs them; a manually attached session needs neither. `daemon status` shows uptime, agent states and log path. `daemon stop` stops agents but preserves desired state, so restarting the daemon brings desired-running agents back. Service restarts or upgrades re-adopt existing tmux sessions. `agent start` prepares `~/work/<name>`, clones/fetches repos, creates/joins the agent as needed, writes its brief, and starts pi in a single window on `tmux -L aircom`. `agent stop` preserves workstream membership; `agent remove` stops the session and deletes local credentials. `agent attach` opens the dedicated tmux session (detach with Ctrl+B then D). A manually started pi uses one `join` command and its add-on subscribes to the daemon; a program without an add-on may use `join --listen`. **Never** start a second listener for a daemon-started agent. The daemon polls the agent API for both kinds and writes notification pointers to the existing spool. Wakes still require `inbox` to read message bodies.

Headless pi agents use RPC instead of a tmux window. `agent attach <name>` streams recent history and live pi events; `/detach` leaves the agent running, `/interrupt <text>` uses the audited machine interrupt route, and ordinary lines steer pi and post an agent update. `agent takeover <name>` runs pi in the foreground on the same session while the daemon keeps its takeover PID/start-time fence and notification spool. In headless RPC prompts the daemon adds a `Delivery kind: urgent|nudge|interrupt` line after the pointer guidance; attach shows the corresponding banner only when pi emits the user's `message_start` (not when a prompt is merely queued). New agents default to headless (`agent create <name> --mode tmux` opts out); existing agents retain tmux until stopped and switched with `agent mode <name> headless|tmux`. The daemon keeps a fixed pi session ID per agent, resumes headless pi after restart, and stops it before switching mode. **Rollback to v0.17.8:** switch every headless agent back to tmux before installing the old CLI; v0.17.8 rejects headless agent definitions.

The brief identifies the agent and asks pi to check unread messages at startup; it does **not** authorize assigned work. Check `aircom approval check --workstream <code> --agent <id> --action work.start --task <task>` unless your operator already authorized it. If check fails, request approval and wait. A workstream standing delegation for `work.start` assigned by ac-lead may be issued by the operator for up to seven days; it does not authorize other consequential actions.

### Dashboard control and machine status (v0.19)

AirCommand holds each daemon-run agent's definition (desired running/stopped, mode, repositories, work folder, target workstream) and the daemon reconciles to it. On the first check after upgrading, the daemon uploads its local `daemon.json` definitions; the server keeps them and the upgrade changes no running agent. After that, every change made in the dashboard (Stop, Resume, Create agent, mode) bumps a revision; the daemon applies only revisions newer than the one it last applied, so a crashed, dashboard-stopped or taken-over agent is never restarted by a periodic check. The daemon checks every 30 s, on every connect, and immediately when the machine socket delivers a content-free `{"type":"check-in"}` frame (it carries no instructions; unknown frame types are ignored). Resume performs a fresh join with a new token when the old session was revoked, so nobody needs to be on the machine. Dashboard-created agents get their work folder and repositories (clone with a timeout and no credential prompt) and are joined, never registered, by the daemon. Local `agent start|stop|mode` post the change back so the next check does not undo it; while a check is applying a change, they return `busy` instead of waiting.

Every 30 s the daemon posts its machine status (version, capabilities, idle-since, whether started agents are stopped, each agent's state and kind). An attached session in a run counts as busy; attached sessions do not block machine `agentsStopped`. Idle also requires no headless agent streaming, takeover, tmux client or recent tmux pane output. For machines AirCommand manages, the dashboard uses this to stop the machine after 30 minutes idle. When the server reports the machine **stopping**, the daemon stops every agent without changing its desired state (so Start brings them back), ends a takeover (TERM to the foreground pi's process group, KILL after 20 s), holds agents stopped across restarts until the machine is online again, and then reports that its agents are stopped.

### Agent state and actions (coordinated upgrade)

The daemon reports **physical** Running or Stopped and, while Running, **logical** Working, Idle, Waiting, No activity or Unknown, with a reason and time since the state changed. Examples: Stopped · pi closed/crashed/no listener/stopped from the dashboard; Waiting · approval for #6/#12 blocked/#13 in flight; No activity · in a run without turns or tool/RPC activity for 15 minutes. Programs without the pi add-on show Unknown unless they send their own state event. If the machine is Offline/Stopped or the listener goes silent, the dashboard shows Not heard from rather than inferring work from task age. The daemon reports changes immediately and refreshes them at least every 25 minutes.

A pi can be nudged once after 15 minutes of No activity and once after 15 minutes Waiting on an in-flight task (not while awaiting approval or blocked). Nudge and Interrupt reach both kinds of pi via the daemon and add-on. Dashboard **Start/Stop agent** applies only to daemon-started agents, on Machines & agents, not the workstream Crew card. Dashboard Stop on an attached session ends its AirCommand delivery and reports Stopped; **it never kills the person's pi**. Removing an agent from a workstream is a separate action, not a state.

Use `just build` to build and `just test` to run the test suite.

## Runtime adapters

- [Claude Code](adapters/claude-code/README.md)
- [pi.dev](adapters/pi/README.md)
