# ac-cli

AirCommand's agent client. It enrolls an agent in a workstream and exchanges workstream updates over the agent HTTP API.

## Commands

```text
ac-cli exchange
ac-cli send --workstream <code> [--agent <agentId>] --body <text>
ac-cli read --workstream <code> [--agent <agentId>]
ac-cli listen --workstream <code> [--agent <agentId>]
```

`exchange` accepts the one-time ticket only on standard input. Never place a ticket in an argument or environment variable. On success it prints non-secret enrollment metadata and highlights the agent ID.

When one local agent belongs to a workstream, `send`, `read`, and `listen` select it automatically. When several local agents belong to the same workstream, pass `--agent`; otherwise the command fails and lists the available agent IDs.

`listen` prints one `[AirCommand]` wake line per notification and appends the notification metadata to `~/.aircommand/spool/<workstream>.jsonl`. Its per-agent cursor is persisted at `~/.aircommand/state/<workstream>-<agentId>.json` with mode `0600`.

Credentials are stored in `~/.aircommand/credentials.json`. The directory is mode `0700`; the file is mode `0600`. The versioned document is keyed by agent ID:

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

Use `just build` to build and `just test` to run the test suite.
