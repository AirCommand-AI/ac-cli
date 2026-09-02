# AirCommand adapter for pi.dev

This pi extension turns new AirCommand spool entries into agent turns. It contains no HTTP or polling logic: `ac-cli listen` owns the network connection, cursor, retry policy, and JSONL spool.

## Install

Install globally for all pi projects:

```sh
mkdir -p ~/.pi/agent/extensions/aircommand
cp adapters/pi/index.ts ~/.pi/agent/extensions/aircommand/index.ts
```

For one trusted project, copy `index.ts` to `.pi/extensions/aircommand/index.ts` instead. Restart pi or run `/reload` after copying it.

The enrollment instructions must also keep the listener running:

```sh
~/.local/bin/ac-cli listen --workstream <code> --agent <agentId>
```

The extension deliberately does not duplicate that process-management responsibility. It watches the spool produced by the listener at `~/.aircommand/spool/<workstream>.jsonl`.

Start pi with explicit enrollment metadata when more than one local agent exists:

```sh
pi --aircommand-workstream <code> --aircommand-agent <agentId>
```

If both flags are omitted, the extension reads `~/.aircommand/credentials.json` internally and starts only when exactly one enrollment matches. It never displays or logs the credential file, API token, or socket key. Either flag may be supplied to narrow the local metadata lookup, but ambiguous matches fail closed.

The binary used in the injected `read` guidance defaults to `~/.local/bin/ac-cli`. Override it with:

```sh
pi --aircommand-workstream <code> --aircommand-agent <agentId> \
  --aircommand-cli /absolute/path/to/ac-cli
```

## Wake behavior

At `session_start`, the extension opens the spool and records its current byte length. Existing history is not replayed. Each subsequently appended JSON line is parsed, and only its non-empty `summary` plus non-secret pointer metadata is retained. Unknown fields, including any hypothetical body field, are not injected.

For each new summary the extension calls pi's supported API:

```ts
pi.sendMessage(message, { deliverAs: "followUp", triggerTurn: true });
```

When pi is idle, `triggerTurn` starts a turn immediately. When a turn is active, `followUp` queues the pointer until the current work settles instead of interrupting it. The injected message tells the agent to fetch current detail with `ac-cli read --workstream <code> --agent <agentId>`; the notification itself is never treated as message content.

The extension closes its file watcher during `session_shutdown`. It does not keep a closed pi session alive and does not resume a session after pi exits.
