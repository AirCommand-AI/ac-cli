# AirCommand add-on for pi

The machine daemon is the only AirCommand connection. `aircom daemon start` installs `index.ts` and `daemon.ts` in `~/.pi/agent/extensions/aircommand/`; restart pi or `/reload` after an upgrade. Project-local copies may shadow the installed add-on and must be updated separately. If the daemon cannot start, ask a person to fix the registration, Linux lingering or macOS desktop login named by the CLI; do not fall back to a direct listener.

From a pi session, run once:

```sh
aircom join --agent <name> --org <org> --workstream <code>
```

The CLI starts the daemon if needed, claims the agent **before** joining, discovers the calling pi process and attaches it. `join` refuses a plain shell with no supported parent program. No background `aircom listen`, manual `/aircommand connect`, or separate direct state report is needed. `join --listen` is for programs without the add-on; never run it for a daemon-started pi. `join` without a workstream asks the daemon to wait for dashboard placement. If another live session holds the identity, join refuses it. Run `aircom leave` before changing workstreams.

The add-on subscribes to the daemon at **every** pi `session_start`, even before an agent is attached, and waits for its `connect` notice. A resumed conversation is found via `session.lookup` and reattached; a new conversation is **never** auto-enrolled. Switching conversations detaches the old one; changing back requires explicit reattachment unless it is an eligible resumed conversation. The daemon, not the add-on, reads the agent's notification file and replays wake lines from the last acknowledged byte offset over the local subscription. The add-on delivers each wake event once, acknowledges its supplied offset and retries a disconnected stream with bounded backoff. Wake lines are pointers; fetch the matching unread message with `aircom inbox` before acting (including messages that predate the subscription). Urgent pointers steer; normal pointers are follow-ups.

Pi sends `run_start`, `turn`, `tool_start`, `tool_end` and `run_end` activity events to the daemon. The daemon determines physical Running/Stopped and, while Running, logical Working/Idle/Waiting/No activity, with reasons and time since each state changed; it refreshes unchanged reports within 25 minutes. No activity begins after 15 minutes without activity inside a run and earns one nudge after another 15 minutes; Waiting on an in-flight task earns one nudge after 15 minutes, but Waiting for an approval or blocked task does not. A daemon `nudge` inserts its text in pi's next turn; `interrupt` aborts the active operation and steers the instruction. A `detached` event stops delivery but **never** terminates a person's pi. Dashboard Start/Stop agent applies only to daemon-started agents, not attached sessions.

`--aircommand-workstream`/`--aircommand-agent` remain explicit startup overrides for daemon-started pi; `--aircommand-headless` disables duplicate spool delivery and token usage in the add-on. `--aircommand-cli` selects the CLI path shown in message guidance. The `/aircommand connect` command and `aircommand_connect` tool remain for manual recovery, not normal joining; both attach through the daemon and are refused while another live session holds the agent.

The daemon socket and per-agent spool live under `~/.aircommand/`; the Unix socket is user-only. The add-on never sends a state report directly to the server. It does not infer authorization from a wake line or message body.
