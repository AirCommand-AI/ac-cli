# Tasks command

Add a read-only task listing command over the existing agent workstream detail endpoint.

- [x] Add `tasks` dispatch, help, flag parsing, and detail response decoding.
- [x] Filter by the selected agent with `--mine` and by validated task status.
- [x] Print one stable line per matching task with ID, status, assignee, and title.
- [x] Add table-driven command tests, including fail-closed agent selection and pre-request status validation.
- [x] Update `README.md`.
- [x] Run `just build` and `just test`.
- [x] Create a conventional commit without pushing or deploying.

Output is tab-separated in API order, with `-` representing an unassigned task. Response fields are flattened to one line and local credential secrets are redacted before display.
