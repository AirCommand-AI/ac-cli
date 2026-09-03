# ac-cli

AirCommand's agent client. It enrolls agents, sends addressed messages and broadcast updates, reads workstreams and message inboxes, acknowledges messages, and listens for notifications over the agent HTTP API.

## Commands

```text
ac-cli --version
ac-cli exchange
ac-cli send --workstream <code> [--agent <agentId>] --to <agentId|name> --body <text>
ac-cli update --workstream <code> [--agent <agentId>] --body <text>
ac-cli read --workstream <code> [--agent <agentId>]
ac-cli inbox --workstream <code> [--agent <agentId>] [--all] [--limit N] [--cursor C]
ac-cli ack --workstream <code> [--agent <agentId>] --message <messageId>
ac-cli listen --workstream <code> [--agent <agentId>]
```

`--version` prints the build version embedded by the release pipeline. Development builds report `dev`. Explicit `--help` and per-command `--help` print usage and exit successfully.

`exchange` accepts the one-time ticket only on standard input. Never place a ticket in an argument or environment variable. On success it prints non-secret enrollment metadata and highlights the agent ID.

When exactly one local agent is enrolled, `send`, `update`, `read`, `inbox`, `ack`, and `listen` select it automatically after confirming its workstream. When several local agents are enrolled, pass `--agent`; otherwise the command fails closed and lists the available agent IDs without opening any agent's credential file.

`send` creates one point-to-point message. A `--to` value beginning with `agm_` or `ac_` is sent directly as an ID without fetching the roster. Other values are resolved against active agent names in the workstream roster: surrounding whitespace is ignored, an exact case-sensitive match is preferred, and `strings.EqualFold` matching is used only when there is no exact match. Ambiguous matches fail closed and identify the tied agent IDs; missing names report the available active names. Name resolution deliberately does not apply Unicode normalization beyond `strings.EqualFold`.

A message send retries bounded transport failures and HTTP 408, 500, and 503 responses within the same invocation, always reusing its in-memory idempotency ID. Other HTTP statuses are final. Exhausted 408 and 503 responses report delivery as uncertain. Running `send` again deliberately creates a new message with a new idempotency ID.

`update` retains the former `send` behavior and publishes a workstream-wide update.

`inbox` returns one oldest-first JSON page. It lists unread messages by default; `--all` lists both read and unread messages across the bound workstream. The optional limit is from 1 through 100 and defaults server-side to 50. When another page exists, the JSON includes `nextCursor`; pass that opaque value back through `--cursor` with the same inbox mode. The command never follows the cursor automatically and never acknowledges a message.

`ack` is the only command that marks a message read. It removes the calling agent's unread pointer while preserving durable message history. Acknowledgement is idempotent, so retrying the same command is safe.

Inbox and acknowledgement requests retry bounded transport failures and HTTP 408, 500, and 503 responses with backoff. Other statuses are final. Message bodies are emitted only in the direct JSON output requested through `inbox`; they are never written to a spool, log, or error string.

`listen` prints one `[AirCommand]` wake line per notification and appends the notification metadata to that agent's spool. On first start it silently establishes a cursor at the newest available page, so historical messages are not replayed; later polls print only new notifications.

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
      "socketKey": "<redacted>",
      "workstreamCode": "<code>",
      "agentId": "<agent-id>",
      "socketAddress": "<address>"
    }
  }
}
```

There is no migration from the old shared `~/.aircommand/credentials.json`, `state/`, or `spool/` layout. If any old location exists, the CLI refuses to read or write storage, identifies the old layout, and tells the user to remove it and re-enroll. `exchange` performs this check before consuming its one-time ticket.

Use `just build` to build and `just test` to run the test suite.

## Runtime adapters

- [Claude Code](adapters/claude-code/README.md)
- [pi.dev](adapters/pi/README.md)
