# ac-cli

AirCommand's agent client. It enrolls an agent in a workstream and exchanges workstream updates over the agent HTTP API.

## Commands

```text
ac-cli --version
ac-cli exchange
ac-cli send --workstream <code> [--agent <agentId>] --body <text>
ac-cli read --workstream <code> [--agent <agentId>]
ac-cli listen --workstream <code> [--agent <agentId>]
```

`--version` prints the build version embedded by the release pipeline. Development builds report `dev`.

`exchange` accepts the one-time ticket only on standard input. Never place a ticket in an argument or environment variable. On success it prints non-secret enrollment metadata and highlights the agent ID.

When exactly one local agent is enrolled, `send`, `read`, and `listen` select it automatically after confirming its workstream. When several local agents are enrolled, pass `--agent`; otherwise the command fails closed and lists the available agent IDs without opening any agent's credential file.

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
