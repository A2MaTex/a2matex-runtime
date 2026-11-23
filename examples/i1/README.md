# I1 — Inspect node attempts and discover screenshots

Use Postman with the actual Runtime and Android target. This guide exercises the
new history endpoint; it does not require SSE or Studio implementation. Start
with one READY device. Two selected devices additionally demonstrate isolated
per-device history. The API contract is in the
[shared integration guide](../../../a2matex-plan/components/a2matex-runtime/integration/managed_runtime.md#node-attempt-history-api--i1).

1. Run `make run` from the Runtime repository and wait for runtime_ready. Obtain
   API_BASE_URL/SESSION_TOKEN using the
   [Linux/Windows authentication guide](../../../a2matex-plan/components/a2matex-runtime/integration/local_authentication.md).
   In Postman use Bearer SESSION_TOKEN for every request. The URL already includes
   `/api/v1`. Probe runtime-info with the current token before the operation.
2. POST `{{API_BASE_URL}}/devices/refresh`, Body none. Select the exact READY
   serial(s), not an IP without its port. Then POST the complete contents of
   [H4 compile-workflow.json](../h4/compile-workflow.json) as raw JSON to
   `{{API_BASE_URL}}/workflow-compilation`. Expect VALID and copy runtimeIr.
3. POST `{{API_BASE_URL}}/runs` as raw JSON. Use the structure below, replacing
   the empty runtimeIr with the complete compiled object and the serial with
   your actual READY value. The empty object is a placeholder, not executable IR.

```json
{
  "requestId": "{{$guid}}",
  "runtimeIr": {},
  "devices": [
    {"adbSerial":"REPLACE_WITH_READY_SERIAL","inputs":{"delayMs":1000}}
  ]
}
```

Expect 201 for a new request. A repeated admitted literal request ID returns200
without rerunning commands. This H4 graph is Start → Delay → End and causes no
visible Android gesture. To add a second device, append another distinct serial
and its own delayMs input. Runtime still validates the complete selection.

4. Save `runGroup.id` as RUN_GROUP_ID. Poll GET
   `{{API_BASE_URL}}/runs/{{RUN_GROUP_ID}}` until SUCCEEDED, then copy the selected
   `deviceExecutions[i].id` to DEVICE_EXECUTION_ID. This is not the group ID or
   ADB serial. GET requests have Body none and need no Content-Type.
5. Send GET `{{API_BASE_URL}}/device-executions/{{DEVICE_EXECUTION_ID}}/attempts`
   with Params afterSequence=0 and limit=1. Continue using the returned sequence.

| Request boundary | Expected attempt | nextAfterSequence |
| --- | --- | --- |
| 0 | start; sequence1; SUCCEEDED; output `{}` | 1 |
| 1 | wait; sequence2; SUCCEEDED; output `{"durationMs":1000}` | 2 |
| 2 | end; sequence3; SUCCEEDED; output `{}` | null |
| 3 | Empty attempts array | null |

Each returned attempt includes id, deviceExecutionId, sequence, nodeId,
attemptNumber, status, startedAt, finishedAt, output and error. Errors and output
are explicit null when absent. The final page's null continuation does not
prove a live workflow finished; that comes from the run snapshot.

6. For active refresh, start a fresh run with delayMs=5000. While wait is running,
   request afterSequence=1. Observe its null output/finishedAt. Repeat the same
   query after completion: the ID and sequence remain unchanged but status,
   output and finishedAt update. Advancing to afterSequence=2 would omit that
   update. Keep observed RUNNING attempts in the refreshed window; upsert by ID.
7. Exercise these negative/read-only cases against the saved execution:

| Case | Expected |
| --- | --- |
| Repeat a completed page | Same saved IDs and values; no extra execution. |
| Known device, afterSequence beyond its last attempt | 200 `{"attempts":[],"nextAfterSequence":null}`. |
| Valid unknown DeviceExecution UUID | 404 RESOURCE_NOT_FOUND. |
| Invalid/uppercase/non-v4 path ID | 400 INVALID_REQUEST. |
| limit0/101, empty/duplicate parameter, unknown key, signed/decimal value | 400 INVALID_REQUEST. |
| Nonempty body, even `{}` | 400 INVALID_REQUEST. |
| Missing/invalid current token | 401 UNAUTHORIZED before route validation. |

History reads require no connected Android endpoint after execution. Disconnect
the target after terminal success and repeat a page; retained history remains
readable. Restart Runtime, reload its token and use the same execution ID; no
old node is dispatched. Do not confuse history in another state directory with
the actual Runtime's store.

For Screenshot output, compile [H15's reference graph](../h15/compile-multi-device.json)
and start a fresh run with inputs `{"score":25,"holdMs":0}` for one READY device.
It performs Home and Screenshot; expect 10 successful attempts. Read history
from0, find capture's output.artifactId, and send Bearer GET
`{{API_BASE_URL}}/artifacts/<that-id>`. Postman's preview/save response displays
the PNG. The history response contains no image bytes or local file path.
With two members, read each one's history using its own execution ID. The same
nodeId does not imply the same attempt/artifact ID.

No SQL is required for this walkthrough. Scope maps remain RAM-only; Set Variable
outputs `{}` and this API does not reveal its assigned value.

Linux automated acceptance can also run without Android:

```bash
go test ./internal/api/attempts ./internal/api -run 'TestHistory|TestG21|TestArtifactDownloadOfG19CommittedPNG' -count=1
go test ./cmd/agent -run '^TestI1AttemptHistoryAgentAcceptance$' -count=1 -v
```

The actual-agent test creates isolated migrated SQLite with retained synthetic
history, uses the real Wire/router and private owner pipes across two sessions,
and verifies no execution-data writes or Android dependency. It does not operate
your normal database/emulator. New test sources are tracked under cmd/agent and
internal/api; no ignored smoke script is needed. Native Windows/live-target UI
acceptance remain separate; existing G22/H14/H15 missing-harness failures remain
open in the full suite.
