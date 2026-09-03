# AirCommand adapter for pi.dev

This pi extension turns new AirCommand spool entries into agent turns. It contains no HTTP or polling logic: `ac-cli listen` owns the network connection, cursor, retry policy, and JSONL spool.

## Install

Install globally for all pi projects:

```sh
mkdir -p ~/.pi/agent/extensions/aircommand
cp adapters/pi/index.ts ~/.pi/agent/extensions/aircommand/index.ts
```

For one trusted project, copy `index.ts` to `.pi/extensions/aircommand/index.ts` instead. Restart pi or run `/reload` after copying it.

The enrollment instructions must also keep the listener process running:

```sh
~/.local/bin/ac-cli listen --workstream <code> --agent <agentId>
```

The extension deliberately does not duplicate that process-management responsibility. It connects a pi session by watching the listener's per-agent spool:

```text
~/.aircommand/agents/<agentId>/spool.jsonl
```

The agent ID path component uses the same sanitisation as `ac-cli`: ordinary `[A-Za-z0-9._-]+` IDs remain readable, except `.` and `..`; reserved `id-` and unsafe values become `id-` plus unpadded URL-safe base64.

## Connect while pi is running

Immediately after `ac-cli exchange` succeeds, the agent calls the registered tool with the exact ID printed by exchange:

```text
aircommand_connect({ "agentId": "<agentId>" })
```

The tool reads only that agent's per-agent `credentials.json` to obtain and validate its workstream, then starts watching its spool from the current end of file. Calling it again for the same agent is harmless. Calling it for another enrolled agent switches this session to the other agent only after the new watcher opens successfully.

A human uses the matching runtime command:

```text
/aircommand connect <agentId>
/aircommand disconnect
```

`disconnect` closes this pi session's spool watcher immediately. It does not stop the separately managed `ac-cli listen` process. A later connect starts at the spool's then-current end and does not replay entries accumulated while disconnected.

There is deliberately no automatic enrollment discovery for restarted runtimes. Reconnect explicitly with the command/tool or use startup flags; persistent runtime identity is an open product decision.

## Startup flags

Startup flags remain an explicit override:

```sh
pi --aircommand-workstream <code> --aircommand-agent <agentId>
```

Supplying both values starts the watcher at `session_start` without reading credentials. Supplying only `--aircommand-agent` reads that agent's per-agent credential metadata to obtain the workstream. A workstream flag without an agent flag is rejected because it cannot identify an isolated agent.

With neither flag, the extension does nothing at startup: it does not inspect AirCommand storage, create a spool, arm a watcher, or display an error. This is the normal behavior for unrelated pi sessions even when the extension is installed globally.

The binary used in injected `read` guidance defaults to `~/.local/bin/ac-cli`. Override it with:

```sh
pi --aircommand-workstream <code> --aircommand-agent <agentId> \
  --aircommand-cli /absolute/path/to/ac-cli
```

## Wake behavior

Whenever a connection starts, the extension opens that agent's spool and records its current byte length. Existing history is not replayed. Each subsequently appended JSON line is parsed, and only its non-empty `summary` plus non-secret pointer metadata is retained. Unknown fields, including any hypothetical body field, are not injected.

For each new summary the extension continues to call pi's verified wake API unchanged:

```ts
pi.sendMessage(message, { deliverAs: "followUp", triggerTurn: true });
```

When pi is idle, `triggerTurn` starts a turn immediately. When a turn is active, `followUp` queues the pointer until the current work settles instead of interrupting it. The injected message tells the agent to fetch current detail with `ac-cli read --workstream <code> --agent <agentId>`; the notification itself is never treated as message content.

The extension closes its file watcher during `session_shutdown`. It does not keep a closed pi session alive and does not resume a session after pi exits.
