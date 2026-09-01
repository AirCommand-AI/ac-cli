# Listen command

Implement T3's long-lived message listener with durable cursors, stdout wake lines, and JSONL spooling.

- [x] Make exchange decoding tolerant of additive response fields
- [x] Add `listen --workstream <code> [--agent <agentId>]`
- [x] Persist per-agent cursors securely across restarts
- [x] Append notifications to the per-workstream spool in sync with stdout
- [x] Enforce sparse actionable output and required failure/recovery lines
- [x] Honor server poll delays with a hard five-second floor
- [x] Add cursor, duplicate, empty-poll, delay, failure, and spool tests
- [x] Update command documentation
- [x] Run formatting, tests, vet, and build validation
