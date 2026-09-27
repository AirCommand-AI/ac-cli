# Activity update CLI

Implement canonical summary/detail updates and task comments while retaining installed legacy flags.

- [x] Add `update --summary [--detail] [--task]` validation, idempotent retries, and safe confirmation output.
- [x] Add task-comment `--summary [--detail]` with validation and mutation exclusivity.
- [x] Keep legacy update `--body` and task `--comment` working through server normalization.
- [x] Add pull-only `events` paging/filtering (`activity` compatibility alias) and document that it creates no wake-ups.
- [x] Update CLI help, README, and runtime adapter guidance.
- [x] Run the full CLI validation suite (`go test ./...`; `git diff --check`).
- [x] Reject simultaneous canonical `--since` and legacy `--after` flags instead of silently taking the last value.
- [x] Preserve legacy `--body` compatibility when its derived summary contains controls, and rerun validation.
