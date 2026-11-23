# H12 — Cancel a group and run again without restarting Runtime

This controlled local demonstration calls the real lifecycle, runner, compiler,
checkpoints and SQLite. Its Home handler counts invocations instead of using ADB.
HTTP actions are H13 and are not exposed by this H12 demo.

From the Runtime root:

```bash
go run ./test_demo/h9-h11-smoke
```

The readable workflow is [the existing graph](../h9-h11/compile-workflow.json):
Start → Delay 5 seconds → Home → Delay 1 second → End, on two controlled devices.
The driver pauses both after the first Delay and prints the SQLite path and group ID.
At the prompt type `cancel` and Enter.

| Observation | Expected |
| --- | --- |
| First group before Cancel | PAUSED, four successful Start/Delay attempts, zero Home actions. |
| After live Cancel | Group/devices CANCELLED, nextNodeId null, the same four successful attempts retained; no extra attempt or Home action. |
| Fresh group | New group/device IDs printed; admitted and executed in the **same owner process** after joined cleanup. |
| Fresh final result | SUCCEEDED, ten successful attempts total, two Home actions. |

The process exits only after that fresh run. Typing `stop` uses intentional
owner shutdown instead; Enter/`resume` retains the original H9–H11 demo.
The driver lives in ignored `test_demo` for local experiments and is not shipped
or required by CI. Focused tracked Go tests provide reproducible verification.

The demo uses its own state directory:
`test_demo/h9-h11-smoke/state/a2matex/runtime/execution.sqlite3`.
It never clears the database and does not touch the main Runtime store. Old demo
records remain; inspect the printed group IDs, not global table totals. The GUI
database viewer can open this file read-only while the demo pauses.

```sql
SELECT id, request_id, status, finished_at FROM run_groups ORDER BY created_at DESC;

-- Bind :group_id to a printed group ID.
SELECT id, adb_serial, status, next_node_id, started_at, finished_at
FROM device_executions WHERE run_group_id = :group_id ORDER BY admission_ordinal;

SELECT a.id, a.sequence, a.node_id, a.status, a.output_json, a.error_json
FROM node_attempts a JOIN device_executions d ON d.id = a.device_execution_id
WHERE d.run_group_id = :group_id ORDER BY d.admission_ordinal, a.sequence;
```

Cancellation interrupts active Delay/commands in normal execution; the demo
above demonstrates cancellation of parked work. Focused tests also cover active,
pre-attempt, retry-gap, control-write, rollback and deadline boundaries.
Stopping an Android command cannot undo an action already received by Android.
Neither this demo nor a Windows cross-build proves native Windows/real-device acceptance.
