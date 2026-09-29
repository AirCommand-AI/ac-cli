# Machine session protocol fixtures

Contract version: design-machine-daemon rev 3 / impl-machine-daemon rev 2, C9–C11 (workstream 626). These fixtures are byte-for-byte identical to as-server-rs/tests/fixtures. Values are illustrative, not required IDs or node names.

- `hello.json`: C9 server-to-daemon WebSocket text frame after successful machine authentication. `generation` is a server-owned number.
- `superseded-close.json`: C9 **WebSocket close event**, code 4001 and reason `superseded`; this is not an application JSON text frame. A revoked or unknown secret instead fails the HTTP upgrade with 401.
- `wake.json`: C10 server-to-daemon text frame with a task notification. `notification` matches the dashboard's `agentapi.MessageNotification` JSON shape, including optional `kind` and `taskId`; it is a pointer, not a message body.
- `wake-minimal.json`: C10 urgent notification without the optional task fields.
- `agent-wake-payload.json`: C11 `agent_wake` task payload (snake_case outer keys, C10 notification nested unchanged). The `acw` AMP message body is this same JSON payload; the AMP message type and task type are transport/envelope metadata, not fields inside the payload. `outbox_sort_key` identifies the durable outbox row; its value here is illustrative.

These examples do not specify the machine secret, HTTP headers, outbox partition key, or any message body. Implementations must not advance the notification cursor on a push.
