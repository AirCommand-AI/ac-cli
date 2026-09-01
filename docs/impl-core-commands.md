# Core agent commands

Implement the initial `ac-cli` repository with secure enrollment exchange and authenticated workstream send/read commands.

- [x] Scaffold the Go 1.25.5 repository and `just` tasks
- [x] Implement credential generation and secure credential persistence
- [x] Implement `exchange`, including stdin-only tickets and idempotent HTTP retries
- [x] Implement authenticated `send` and `read`
- [x] Map API failures to safe, actionable messages without leaking response bodies
- [x] Add unit and `httptest` integration coverage
- [x] Run formatting, tests, and build validation
- [x] Commit the implementation in the new repository

The exchange success schema was not specified, so the decoder accepts common flat, nested, and `data`-wrapped metadata fields. Credential lookup fails closed if more than one stored agent matches a workstream.
