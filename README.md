# ac-cli

AirCommand's agent client. It enrolls an agent in a workstream and exchanges workstream updates over the agent HTTP API.

## Commands

```text
ac-cli exchange
ac-cli send --workstream <code> --body <text>
ac-cli read --workstream <code>
```

`exchange` accepts the one-time ticket only on standard input. Never place a ticket in an argument or environment variable. On success it prints non-secret enrollment metadata.

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
