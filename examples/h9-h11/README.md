# H9–H11 — Internal Pause/Resume demonstration

The runtime checkpoint and live-worker controls are implemented. The public
action endpoint belongs to H13 and is not available yet. This demonstration
calls the normal internal services and uses real SQLite with two **controlled**
endpoints. It sends no command to Waydroid/Android and needs no ADB installation.

From the Runtime repository on Linux or Windows:

```sh
go run ./test_demo/h9-h11-smoke
```

The local driver is [main.go](../../test_demo/h9-h11-smoke/main.go). The repository
ignores `test_demo/`, so this driver/state is local to this checkout; the tracked
regression tests remain under `internal/repositories` and
`internal/services/executionlifecycle`. The driver compiles and validates the
committed [workflow](compile-workflow.json) rather than bypassing its contracts.

1. The driver prints the SQLite path and a fresh RunGroup UUID. It uses its own
   `test_demo/h9-h11-smoke/state` directory, preserves earlier records, and never
   clears or writes the main agent database.
2. Both devices execute Start → Delay 1 (five seconds). The driver requests
   Pause after both Delays start. Pause remains pending while those Delays finish.
3. At PAUSED, expect two paused members with `nextNodeId=home`, four successful
   attempts total and **zero Home actions**. The printed snapshot has null
   finishedAt fields. It stays stable while you inspect the database.
4. Press Enter to Resume. Expect exactly two `CONTROLLED Home action` lines,
   followed by Delay 2 and End. The same group/device IDs finish SUCCEEDED with
   ten successful attempts total. Start and Delay 1 are not repeated.
5. Alternatively type `stop` and Enter while paused. The process commits
   CANCELLED for the unfinished group/members before closing SQLite. Its four
   successful attempts remain unchanged. EOF also chooses stop; it never resumes
   work automatically because stdin was closed.

The same command can run from `test_demo/h9-h11-smoke` as `go run .`.
Compiler/validator fixture compatibility is pinned to harness version 0.1.0.

## Inspect in DataGrip or another SQLite viewer

Open the **printed** execution.sqlite3 file; keep the generated RunGroup ID and
replace the placeholder below. These queries are read-only and group-scoped:

```sql
SELECT id, status, created_at, finished_at
FROM run_groups WHERE id = '<run-group-id>';

SELECT id, adb_serial, status, next_node_id, started_at, finished_at
FROM device_executions WHERE run_group_id = '<run-group-id>'
ORDER BY admission_ordinal;

SELECT d.adb_serial, a.sequence, a.node_id, a.attempt_number, a.status
FROM node_attempts a JOIN device_executions d ON d.id = a.device_execution_id
WHERE d.run_group_id = '<run-group-id>'
ORDER BY d.admission_ordinal, a.sequence;

SELECT cursor, event_type, device_execution_id, payload_json
FROM execution_events WHERE run_group_id = '<run-group-id>' ORDER BY cursor;
```

At Pause, each member has successful `start` and `delay_1` attempts. After Resume,
each has exactly `start`, `delay_1`, `home`, `delay_2`, `end`, sequences 1–5 and
attemptNumber=1. One pause/resume cycle changes two device states and one group
state per control transaction: six control events total. Resume's subsequent
normal QUEUED → RUNNING begin events are separate. Global cursor values can
include previous runs, and event ordering between devices can interleave.

Linux smoke verification observed four stable completed attempts/zero actions
while PAUSED, then ten successful attempts/two controlled actions and 44 events
for that completed group. A separate stop-at-pause run preserved four attempts
and settled CANCELLED. This is not real-ADB or native-Windows acceptance.

The main spec is [H9–H11](../../../a2matex-plan/components/a2matex-runtime/h_multi_device_execution/h9_h11_pause_resume.md).
The future H13 API will make the same lifecycle behavior available to Postman,
Studio and Launcher; do not add temporary HTTP control routes for this demo.
