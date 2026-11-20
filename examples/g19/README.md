# G19 Screenshot API check

Use the running Runtime with a compatible version, configured ADB, and one
READY device. Screenshot is now executable through the normal workflow API.
Build with `make build` and restart the built Runtime after updating the code;
an already-running process keeps its previous executable registry.
The [authentication guide](../../../a2matex-plan/components/a2matex-runtime/integration/local_authentication.md)
explains how to find apiBaseUrl and attach the session token in Postman.

1. Send `GET {apiBaseUrl}/runtime-info`. Check for the exact
   `android.screenshot` type/runtimeHandler pair.
2. Send the complete [compile-screenshot.json](compile-screenshot.json) body to
   `POST {apiBaseUrl}/workflow-compilation`. Expect valid Runtime IR.
3. Paste that exact runtimeIr object into the existing
   [POST /runs request](../../../a2matex-plan/components/a2matex-runtime/integration/managed_runtime.md#single-device-start-api),
   with a **new lowercase UUIDv4 requestId**, one exact READY adbSerial,
   and `inputs: {}`. Expect HTTP 201 and retain runGroup.id.
4. Poll `GET {apiBaseUrl}/runs/{runGroup.id}` until SUCCEEDED. The snapshot
   reports state; the Screenshot output is in the stored attempt history.

Expected order: **start → wait → capture → end**, four successful attempts.
The one-second Delay gives you time to observe the run. A Screenshot reads
the display and does not visibly change Android's screen.

Open the Runtime's normal execution.sqlite3 in DataGrip and substitute the
run ID in this read-only query:

```sql
SELECT n.sequence, n.node_id, n.status, n.output_json,
       a.id AS artifact_id, a.file_key, a.byte_length,
       a.width, a.height, a.digest
FROM node_attempts AS n
JOIN device_executions AS d ON d.id = n.device_execution_id
LEFT JOIN artifacts AS a ON a.attempt_id = n.id
WHERE d.run_group_id = 'REPLACE_WITH_RUN_GROUP_ID'
ORDER BY n.sequence;
```

Only capture has an artifact row. Its output is exactly
`{"artifactId":"<the artifact row ID>"}`. Open that row's file_key under the
Runtime state directory's artifacts folder with your host image viewer:

| Host    | Default artifact directory                                        |
| ------- | ----------------------------------------------------------------- |
| Linux   | `${XDG_STATE_HOME:-$HOME/.local/state}/a2matex/runtime/artifacts` |
| Windows | `%LOCALAPPDATA%\A2MaTeX\Runtime\artifacts`                        |

Use the actual configured state location if overridden. PNG files remain after
the run finishes. They are not copied into Android Gallery or the smoke-test
directory. G20 now also supports authenticated image retrieval through HTTP.

## Download the PNG in Postman (G20)

With a G20 build, use the capture row's artifact_id in a new request:

- Method: GET.
- URL: `{apiBaseUrl}/artifacts/<artifact_id>`.
- Authorization: Bearer Token, using the current Runtime session token.
- Params: empty. Body: none.

Expect **200**, `Content-Type: image/png`, `Cache-Control: no-store`, and
Content-Length equal to the query's byte_length. Preview the image or save the
response as a PNG in your chosen host folder. Check its dimensions against
width/height. This is binary image data, not JSON or Base64. Repeating the GET
returns the stored image without ADB or another capture. Historical committed
images remain downloadable after restart using the new session token.

Run snapshots do not expose per-node outputs yet. The SQL above is a manual
test aid; Studio must obtain IDs through the later A7/I1 committed-output
transport, not by querying SQLite. See the
[shared download reference](../../../a2matex-plan/components/a2matex-runtime/integration/managed_runtime.md#screenshot-artifact-download-api)
for errors, cancellation, incomplete transfers, and native/UI ownership.

## Capture a region

To test a region, replace capture's config and bindings, recompile, and start
with a new requestId:

```json
{
  "config": { "targetMode": "SCREEN_PERCENT" },
  "bindings": {
    "startXPercent": { "kind": "LITERAL", "value": 20 },
    "startYPercent": { "kind": "LITERAL", "value": 20 },
    "endXPercent": { "kind": "LITERAL", "value": 40 },
    "endYPercent": { "kind": "LITERAL", "value": 30 }
  }
}
```

On a 1920×1080 frame this stores a 384×108 image. For absolute pixels use
ABSOLUTE_PIXELS and literal startX/startY/endX/endY. All four coordinates are
required; the top-left corner must precede the bottom-right corner on both
axes and the rectangle must fit the captured frame.

Resending an existing successful requestId returns HTTP 200 for the old run
and takes no new screenshot. Use the
[G19 acceptance scenarios](../../../a2matex-plan/components/a2matex-runtime/g_workflow_engine/g19_screenshot_execution.md#113-small-manual-variants)
for failure/error-route variants. Runtime restart interrupts unfinished runs;
it never repeats their captures.
