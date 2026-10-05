# AirCommand add-on for pi

The machine daemon is the only AirCommand connection. `aircom daemon start` installs `index.ts` and `daemon.ts` in `~/.pi/agent/extensions/aircommand/`; restart pi or `/reload` after an upgrade. Project-local copies may shadow the installed add-on and must be updated separately.

From a pi session, run once:

```sh
aircom join --agent <name> --org <org> --workstream <code>
```

The CLI starts the daemon if needed, claims the agent, joins, discovers this pi's parent process and attaches it. No background `aircom listen`, manual `/aircommand connect`, or separate direct state report is needed. `join` without a workstream asks the daemon to wait for dashboard placement. If another live session holds the identity, join refuses it. Run `aircom leave` before changing workstreams.

The add-on subscribes to the daemon at every pi `session_start`, even before an agent is attached. The daemon announces `connect` after the CLI attaches. A resumed conversation is found via `session.lookup` and reattached; a new conversation is **never** auto-enrolled. The add-on replays the agent's notification file from the daemon's last acknowledged byte offset, acknowledges consumed lines and retries a disconnected local stream with bounded backoff. Wake lines are pointers; fetch the matching unread message with `aircom inbox` before acting. Urgent pointers steer; normal pointers are follow-ups.

Pi sends `run_start`, `turn`, `tool_start`, `tool_end` and `run_end` activity events to the daemon. The daemon determines physical/logical presence and reports it to AirCommand. A daemon `nudge` inserts its text in pi's next turn; `interrupt` aborts the active operation and steers the instruction. A `detached` event stops the local watcher but **never** terminates pi.

`--aircommand-workstream`/`--aircommand-agent` remain explicit startup overrides for daemon-started pi; `--aircommand-headless` disables duplicate spool delivery and token usage in the add-on. `--aircommand-cli` selects the CLI path shown in message guidance. The `/aircommand connect` command and `aircommand_connect` tool remain for manual recovery, not normal joining.

The daemon socket and per-agent spool live under `~/.aircommand/`; the Unix socket is user-only. The add-on never sends a state report directly to the server. It does not infer authorization from a wake line or message body.
