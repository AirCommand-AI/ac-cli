# Per-agent storage

Implement W6 by isolating credentials, listener state, and notification spools beneath one sanitized directory per agent.

- [x] Add a shared, traversal-safe agent path component helper
- [x] Store one enrollment at `agents/<agentId>/credentials.json`
- [x] Store listener cursor and spool beside that agent's credentials
- [x] Reject legacy storage with clear re-enrollment guidance
- [x] Test secure modes, crafted IDs, legacy detection, and two-agent path isolation
- [x] Update storage documentation
- [x] Run formatting, tests, vet, and build validation

When more than one local agent exists, omitting `--agent` now fails before any credential file is opened. Scanning credentials by workstream would read other agents' secrets and violate CON-6.
