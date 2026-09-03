# Listen command

Implement T3's long-lived message listener with durable cursors, stdout wake lines, and JSONL spooling.

- [x] Make exchange decoding tolerant of additive response fields
- [x] Add `listen --workstream <code> [--agent <agentId>]`
- [x] Persist per-agent cursors securely across restarts
- [x] Append notifications to the per-agent spool in sync with stdout
- [x] Enforce sparse actionable output and required failure/recovery lines
- [x] Honor server poll delays with a hard five-second floor
- [x] Add cursor, duplicate, empty-poll, delay, failure, and spool tests
- [x] Update command documentation
- [x] Run formatting, tests, vet, and build validation
- [x] Treat the first successful poll without stored state as a silent cursor baseline
- [x] Test history suppression, one later wake, and restart deduplication

W9 moves `listen` to the addressed-message notification feed while preserving its established lifecycle behavior.

- [x] Poll `/agent/v1/workstreams/{code}/notifications` with exact cursor semantics
- [x] Decode pointer-only `message.received` notifications
- [x] Compose a non-empty human-readable summary for stdout and the spool
- [x] Resolve sender names through one lazy, invocation-local roster cache
- [x] Fall back to structural sender IDs when no cached name is available
- [x] Preserve silent baseline, one-line-per-event output, poll floor, and per-agent persistence
- [x] Retry and visibly report transport and contract-retryable feed failures without advancing the cursor
- [x] Preserve terminal 401/404 behavior and map invalid cursor failures safely
- [x] Add route, shape, cache, fallback, baseline, retry, cursor, spool, and regression tests
- [x] Update listener documentation and run full validation

Composed summary format: `New message from <sender-name-or-id> (<nature>) in workstream <code>: <messageId>; run ac-cli inbox.` The roster is loaded lazily at most once per listener invocation; failures and unknown identities fall back to the server-supplied sender ID without suppressing the wake.
