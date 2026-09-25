# Quiet through brief network drops

Issue: [ac-cli#5](https://github.com/AirCommand-AI/ac-cli/issues/5)

## Problem

`aircom listen` printed `Lost connection: …` on every failed poll and
`Connection restored.` on the next success. Each stdout line becomes a wake-up
in the agent's runtime, so a few seconds' network hiccup woke Claude-lead twice
and Scout three times on 2026-09-24, each costing a turn.

## Change

`internal/app/app.go`, listener only.

- `listenOutage` tracks one run of failed polls: when it started and whether it
  has been announced. `failed(now)` announces the run once it has lasted
  `outageAnnounceAfter` (60 seconds), and never again for the same run;
  `recovered()` ends the run and announces recovery only if the loss was
  announced.
- Both failure paths — transport errors and retryable HTTP (408, 500, 503) —
  go through `noteListenFailure`, which prints `Lost connection: <reason>` only
  when the outage says so. The reason is from the failure that crossed the
  minute.
- Retrying is unchanged: same backoff (5, 10, 20, then 30 seconds) and the
  cursor is not advanced by failed polls.
- Terminal statuses (401, 404, other non-retryable HTTP) are unchanged: printed
  and exited at once, whether or not an outage is in progress.
- Time comes from a new injectable `App.ListenNow` (default `time.Now`, which
  is monotonic within the process). Tests drive it with `ListenSleep`, so a
  multi-minute outage runs instantly.

With the backoff, failures land at 0, 5, 15, 35 and 65 seconds, so the first
announcement is on the fifth consecutive failure, about 65 seconds in.

## Output effects

- stdout: at most one `Lost connection` and one `Connection restored.` per
  outage lasting a minute or more; nothing for shorter ones. Notification wake
  lines are unaffected, including one arriving right after an outage.
- stderr: unchanged.
- spool: unchanged — outage lines were never spooled and still are not.

## Tests

`internal/app/listen_outage_test.go`, scripted feed plus a fake clock:
brief HTTP and network outages are silent; prolonged HTTP and network outages
print one loss and one recovery; a later brief outage stays silent; a message
after a brief or a prolonged outage is still delivered; backoff, cursor and
spool are unchanged through an outage; a 401 during an outage stops at once
with only the terminal line. The two existing retry tests in `listen_test.go`
now expect no output for their brief outages. Setting the threshold to zero
makes the brief-outage tests fail.

## Validation

`just test` (`go test ./...`) and `go vet ./...` pass.
