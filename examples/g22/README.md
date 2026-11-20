# G22 combined single-device workflow

This example combines the shipped execution features in one graph:

**Start → Home → Tap → Swipe → Long Press → Input Text → Screenshot →
Set Flow variable → Set Global variable → Switch → Delay → End.**

Screenshot's artifactId is assigned to a Flow variable, copied into a Global
(RUN_GROUP) variable, and compared by Switch. Equal values select Delay;
Switch's default and Screenshot's error branch select fallback → End.
The successful path has **12 successful attempts**, no fallback attempt, and
one committed cropped PNG. Both Set Variable nodes and Switch return `{}`.

## Automated acceptance

From the Runtime repository, run:

```bash
go test ./cmd/agent -run '^TestG22CombinedEngineAcceptance$' -count=1 -v
```

This Linux process test requires Python 3, using its standard library only.
It builds a versioned Runtime in a temporary directory and starts the real
binary with private owner pipes, real loopback HTTP, migrations, and SQLite.
Only the ADB executable is controlled. It does not use the user's database,
installed ADB server, or Android device. It verifies success/replay, safe retry,
no unsafe retry, handled capture failure, run isolation, client disconnect,
shutdown/owner loss/crash, rollback, and historical image downloads. The
[G22 spec](../../../a2matex-plan/components/a2matex-runtime/g_workflow_engine/g22_workflow_engine_acceptance.md)
records exact evidence and remaining native/device checks.

## Manual Postman check on Linux or Windows

Use a disposable emulator screen: the graph sends real Home, Tap, Swipe, Long
Press, and Input Text commands. Its success means those handlers completed;
inspect the visible result and PNG separately. For meaningful text entry,
edit the gesture coordinates to focus a text field before Input Text.

1. Start your normal Runtime with a configured real ADB executable. Follow the
   [authentication guide](../../../a2matex-plan/components/a2matex-runtime/integration/local_authentication.md)
   for the base URL and current Bearer token. Refresh devices and select one READY
   serial through the [device API](../../../a2matex-plan/components/a2matex-runtime/integration/managed_runtime.md#device-inventory-api).
2. Open [compile-workflow.json](compile-workflow.json). Edit literal gesture
   coordinates/durations for your screen, then POST the complete file to
   `{apiBaseUrl}/workflow-compilation`. Expect `validation.status: VALID` and
   retain the exact returned runtimeIr. Percentage gestures use the current
   logical display size; the crop uses the captured frame dimensions.
3. POST `{apiBaseUrl}/runs` with a fresh lowercase UUIDv4 requestId, that runtimeIr,
   and exactly one device:

   ```json
   {
     "requestId": "REPLACE_WITH_NEW_UUIDV4",
     "runtimeIr": "REPLACE_THIS_STRING_WITH_THE_COMPLETE_IR_OBJECT",
     "devices": [
       {
         "adbSerial": "REPLACE_WITH_READY_SERIAL",
         "inputs": { "holdMs": 1000, "text": "hello" }
       }
     ]
   }
   ```

   The runtimeIr placeholder must be replaced by a JSON object. Expect HTTP 201.

4. Poll GET `{apiBaseUrl}/runs/{runGroupId}`. Expect SUCCEEDED, total=1,
   succeeded=1, other counters zero, terminalError=null, and nextNodeId=null.
   In DataGrip, inspect attempts using the [G19 query](../g19/README.md).
   Expect the 12-node order above and one artifact attached to capture.
5. Copy its artifact_id for this manual check, then GET
   `{apiBaseUrl}/artifacts/{artifact_id}` with the same Bearer token and no
   body/query. Preview or save the PNG. The region is from 20%/20% to 40%/30%
   of the captured frame; check the stored dimensions and visible content.
   Automatic ID delivery to Studio remains A7/I1; client code must not use SQLite.
6. Resend the exact POST /runs body. Expect HTTP 200 with the same group ID,
   unchanged attempt/artifact counts, and no repeated actions. Use a new requestId
   for another execution. Every new group gets fresh Flow/Global RAM scopes.

For a stop check, use holdMs=60000 and wait until the final Delay is active.
Use the owner's Stop and exit action/private shutdown channel. After restart,
use the new token: the old run is CANCELLED and its committed PNG is readable.
Owner loss/forced exit instead produces INTERRUPTED on startup; old runs never
resume automatically. The automated test uses isolated child processes for
these failures; do not kill unrelated processes or alter normal user storage.
