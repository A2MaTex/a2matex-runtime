# G18 manual API check

This example uses the real Set Variable and Switch engine path. It changes no
Android screen. One READY ADB device is still required by run admission.

1. Build with `make build`, then start `bin/a2matex-runtime` on Linux or the
   built `a2matex-runtime.exe` on Windows. The build must satisfy the compiled
   IR version requirement. Use the normal configured ADB path.
2. Follow the [authentication guide](../../../a2matex-plan/components/a2matex-runtime/integration/local_authentication.md)
   to get `apiBaseUrl` and the session token. In Postman use Bearer Token and
   `Content-Type: application/json` for POST requests.
3. Send `GET {apiBaseUrl}/runtime-info`. Check that `supportedNodes` includes
   `flow.set_variable` paired with the same runtime handler.
4. Send the entire [compile-score.json](compile-score.json) body to
   `POST {apiBaseUrl}/workflow-compilation`. Expect a valid response containing
   `runtimeIr`.
5. Paste that exact `runtimeIr` object into `POST {apiBaseUrl}/runs`, using the
   request shape below. Replace the request UUID and exact READY device serial.

```json
{
  "requestId": "a3526b0c-b066-43d9-a681-5630b4469e87",
  "runtimeIr": {},
  "devices": [
    {
      "adbSerial": "REPLACE_WITH_READY_SERIAL",
      "inputs": { "score": 30 }
    }
  ]
}
```

The empty `runtimeIr` above is only a paste location; it cannot be submitted
unchanged. Expect HTTP 201 and retain `runGroup.id`. Poll
`GET {apiBaseUrl}/runs/{runGroup.id}` until `SUCCEEDED`.

| Input       | Expected attempt order                                       |
| ----------- | ------------------------------------------------------------ |
| `score: 30` | start → set_score → set_threshold → choose → low_wait → end  |
| `score: 75` | start → set_score → set_threshold → choose → high_wait → end |

Repeat the second case with a **new requestId**. Resending an existing ID
returns HTTP 200 for the old run and executes nothing again. The two setters
and Switch have `{}` output. Branch selection proves their values were
available in RAM. The example delays are one/two seconds; exact timings vary.

Inspect the normal Runtime SQLite store in DataGrip:

```sql
SELECT a.sequence, a.node_id, a.status, a.output_json, a.error_json
FROM node_attempts AS a
JOIN device_executions AS d ON d.id = a.device_execution_id
WHERE d.run_group_id = 'REPLACE_WITH_RUN_GROUP_ID'
ORDER BY a.sequence;
```

Variables have no SQLite table. Each new run starts empty, and terminal worker
cleanup releases its maps. Runtime exit loses variables; old unfinished runs
are interrupted after restart and never resumed.

For replacement, failed-overwrite, and empty-run variants, follow the
[G18 manual scenarios](../../../a2matex-plan/components/a2matex-runtime/g_workflow_engine/g18_set_variable_execution.md#113-replacement-failed-assignment-and-fresh-run-checks).
Public multi-device scheduling, Pause/Resume, and Wait Variable remain Phase H.
