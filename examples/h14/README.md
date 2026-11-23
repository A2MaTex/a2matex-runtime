# H14 — Inspect shutdown and recovery

This acceptance pack runs the **actual built Runtime** with private owner pipes,
normal authenticated HTTP, real migrated SQLite and a controlled ADB executable.
No emulator or ADB installation is needed. It tests process ownership and command
dispatch, not real Android side effects or native Windows behavior.

From the Runtime root:

```bash
make build
python3 cmd/agent/testdata/h14_acceptance.py bin/a2matex-runtime --results-dir test_demo/h14-results
```

Choose a new/empty results directory. The script refuses to clear existing data.
The default run performs 12 scenarios and prints PASS lines. For one scenario:

```bash
python3 cmd/agent/testdata/h14_acceptance.py bin/a2matex-runtime --scenario crash-active --results-dir test_demo/h14-crash-results
```

Without `--results-dir`, the script uses an automatically cleaned temporary
directory. The Go test wrapper selects isolated temporary results for CI:

```bash
go test ./cmd/agent -run '^TestH14ShutdownRecoveryAgentAcceptance$' -count=1 -v
```

## Workflow and controlled devices

Read [compile-workflow.json](compile-workflow.json):
Start → Delay from input → Press Home → full Screenshot → End.
Every scenario compiles that graph through the real API and submits its returned
IR through POST /runs. Both controlled serials are selected explicitly.

In mixed cases, `device-0`'s Home invocation waits on a fixture file while
`device-1` finishes and commits its screenshot. Pause permits the in-flight Home
to finish, then parks before Screenshot. This creates clear terminal-sibling
and unfinished-work evidence without a production debug endpoint.

The controlled ADB writes and flushes `dispatch.jsonl` before executing each
Home/Screenshot fixture command. Home also records completion. Runtime and its
SQLite rows alone cannot prove that an external operation was dispatched once;
the independent journal supplies that evidence. Discovery/readiness probes are
not counted as workflow actions.

## Scenarios and expected outcomes

| Scenario | Expected old group on reopen |
| --- | --- |
| `idle` | No execution events added; clean stop/restart releases store ownership. |
| `delay-stop` | Both active Delay attempts/devices CANCELLED; Start attempts retained; no Home/Screenshot dispatch. |
| `command-stop` | A's active Home CANCELLED; B's successful history and committed PNG retained. |
| `paused-stop` | Paused A cancelled without a new attempt or Resume; B retained. |
| `pause-pending-stop` | Stop interrupts the blocked command, joins pending Pause and cancels unfinished work. |
| `cancel-stop` | Accepted live Cancel and intentional shutdown settle/join; B retained. |
| `cancel-owner-loss` | Earlier explicit Cancel remains authorized despite later bare owner EOF. |
| `owner-loss-active` | A's unfinished Home/device/group become INTERRUPTED at next startup; B retained. |
| `owner-loss-paused` | Paused A/device/group become INTERRUPTED; no new attempt. |
| `crash-active` | Forced Runtime kill leaves A's invocation uncertain; next startup interrupts its existing attempt without redispatch. |
| `crash-paused` | Forced kill interrupts the paused member on restart; no Resume or screenshot recapture. |
| `recovery-failure` | Injected event-write fault rolls back recovery and blocks readiness/HTTP; removing that fixture-only fault permits a successful interruption. |

Every non-idle case also reopens a second time, replays the original request ID
without dispatch, and starts a fresh group successfully. The new group performs
exactly two Home and two Screenshot operations. The original group's history
does not change during that fresh run.

Graceful Runtime exit must join its owned ADB clients. For a forcibly killed
Runtime, the harness explicitly terminates only its own journal-identified
fixture clients; it does not pretend the dead parent could have performed
graceful cleanup. No real ADB server or Android/emulator process is stopped.

## Read the evidence

Each scenario has its own folder under your chosen results directory:
The top-level `report.json` records host/architecture, binary and fixture hashes,
controlled-device scope and scenario results. It contains no session credentials.

| File | Meaning |
| --- | --- |
| `evidence.json` | Old/fresh group IDs, final outcome and expected dispatch/attempt counts; non-idle cases. |
| `before-exit.json` | Consistent pre-exit group/device/attempt/event/artifact rows with named columns. |
| `after-recovery.json` | Same projections after reopen; compare statuses and unchanged terminal records. |
| `request-<groupId>.json` | Exact original/fresh POST body; no credentials. Old request remains available for replay inspection. |
| `dispatch.jsonl` | Persistent command-entry/completion journal, including selected serial and fixture process ID. |
| `agent-1.log`, `agent-2.log`, … | Runtime lifecycle logs for each successive process, without readiness credentials. |
| `a2matex/runtime/execution.sqlite3` | Retained real SQLite database. Open read-only in DataGrip. |

Inspect `evidence.json` first, then the named-column JSON or database. Timestamps
in SQL are integer milliseconds. Event cursors are global; compare a particular
group's events rather than expecting all counters to restart from one.

```sql
SELECT id, request_id, status, finished_at FROM run_groups ORDER BY created_at;

SELECT id, adb_serial, status, next_node_id, started_at, finished_at
FROM device_executions WHERE run_group_id = :group_id ORDER BY admission_ordinal;

SELECT a.id, a.sequence, a.node_id, a.status, a.output_json, a.error_json
FROM node_attempts a JOIN device_executions d ON d.id = a.device_execution_id
WHERE d.run_group_id = :group_id ORDER BY d.admission_ordinal, a.sequence;

SELECT cursor, event_type, payload_json
FROM execution_events WHERE run_group_id = :group_id ORDER BY cursor;
```

The full [H14 spec and S1–S15 evidence map](../../../a2matex-plan/components/a2matex-runtime/h_multi_device_execution/h14_scheduler_shutdown_and_recovery.md)
also identifies reused service/repository tests for writer races, deadlines,
admission fencing and large membership. H15 owns broader independent-device
acceptance; cross-compilation alone does not verify native Windows execution.
