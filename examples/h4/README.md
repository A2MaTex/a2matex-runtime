# H4 — Multi-device workflow start

Run the same Start → Delay → End workflow on all selected devices, with a
separate `delayMs` input per device. This example sends no Android UI command;
ADB readiness is still required. One-device requests remain valid.

1. Start a versioned Runtime with the correct ADB path. Linux: `make build`,
   then run `./bin/a2matex-runtime` with the existing ADB configuration.
   Windows: use the installed `a2matex-runtime.exe` and Windows platform-tools.
   Follow the [authentication guide](../../../a2matex-plan/components/a2matex-runtime/integration/local_authentication.md)
   for the current connection URL/token. Managed apps read their private ready record.
2. In Postman, set the published API base URL and Bearer token. Call
   `POST /devices/refresh` with no body. Select exact READY serials.
3. Open [start-run.json](start-run.json), replace both example serials, and
   generate a fresh lowercase UUID v4 for `requestId`. Edit each `delayMs`
   independently (0–10000). With only Waydroid, keep one array entry; a normal
   Waydroid instance is not two independent devices. There is no maximum-device
   setting to configure.
4. POST that full JSON to `/runs` with `Content-Type: application/json`.
   Expect 201 and a snapshot containing all selected devices. POST waits for
   admission/ownership, not workflow completion; a fast run may already be terminal.
5. Copy `runGroup.id`; GET `/runs/{runGroupId}` to poll. Expect one group with
   total=2 and eventually succeeded=2 for the unchanged two-device example.
   Each node sequence is independent; the 1000ms member can finish while the
   3000ms member continues. GET is JSON polling, not SSE.
6. Resend the identical POST: expect 200 with the same group/device IDs and no
   second execution. Change `requestId` only for a deliberate new run.

To compile your own graph, POST [compile-workflow.json](compile-workflow.json)
to `/workflow-compilation`, then paste the returned `runtimeIr` object into
`start-run.json`. Do not paste the entire compilation response. The supplied
complete IR is also validated by the actual-agent H4 acceptance test.

## Rejection checks

Use a fresh requestId for each check. Empty selection or duplicate/malformed
serial returns 400. Invalid inputs for a later member return 422 with validation
paths containing its `devices` index. A missing/offline/not-ready device returns
422 with `unavailableDevices`: zero-based index, submitted serial and safe reason.
Every rejection creates zero partial runs and starts no selected device.
Refresh/correct the selection; never silently remove the failed member.

## Optional read-only SQLite check

For a successful two-device run, filter by its group ID:

```sql
SELECT id, status FROM run_groups WHERE id = :run_group_id;
SELECT id, adb_serial, admission_ordinal, status
FROM device_executions WHERE run_group_id = :run_group_id ORDER BY admission_ordinal;
SELECT count(*) FROM node_attempts a JOIN device_executions d
  ON d.id = a.device_execution_id WHERE d.run_group_id = :run_group_id;
SELECT count(*) FROM execution_events WHERE run_group_id = :run_group_id;
```

Expected: one SUCCEEDED group, two SUCCEEDED devices in request order, six
attempts and 24 events. One device gives three attempts/13 events. GET/replay
adds no writes; active workers can independently advance while a request runs.
SQL is a manual test aid, not a Studio/Launcher integration dependency.

The ordinary HTTP/race tests and `TestH4MultiDeviceAgentAcceptance` use isolated
SQLite and controlled endpoints. This does not prove native Windows or real
multi-device Android execution. The existing G22 harness remains missing.

See the [shared API/process guide](../../../a2matex-plan/components/a2matex-runtime/integration/managed_runtime.md#workflow-start-api),
[Studio guide](../../../a2matex-plan/components/a2matex-runtime/integration/studio.md)
and [H4 specification](../../../a2matex-plan/components/a2matex-runtime/h_multi_device_execution/h4_public_multi_device_start.md).
Production encrypted-package authorization, live run controls and SSE remain later work.
