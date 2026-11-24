# I4 — Follow workflow events

The authenticated `GET /api/v1/events` endpoint is implemented. It streams saved
A6 execution changes, not operational logs, over one persistent connection.
Closing a monitor does not cancel a workflow or stop Runtime.

1. Start the built Runtime normally. In development, `make build` followed by
   `./bin/a2matex-runtime` uses the embedded Makefile version. No Android device
   is required for the idle-stream check. Native apps use their installed child
   and the owner handshake instead.
2. Obtain the current API URL and token using the
   [Linux/Windows authentication guide](../../../a2matex-plan/components/a2matex-runtime/integration/local_authentication.md).
   Set `API_BASE_URL` and `SESSION_TOKEN` in your shell, or the same variables
   in Postman. The Runtime does not set variables in another process's shell.
3. Open the connection using either command below. The base URL already ends
   in `/api/v1`.

Linux:

```bash
curl -N --http1.1 "$API_BASE_URL/events" \
  -H "Authorization: Bearer $SESSION_TOKEN" \
  -H "Accept: text/event-stream"
```

Windows PowerShell:

```powershell
curl.exe -N --http1.1 "${env:API_BASE_URL}/events" `
  -H "Authorization: Bearer $env:SESSION_TOKEN" `
  -H "Accept: text/event-stream"
```

Expect HTTP 200 immediately. The body may be empty initially. After roughly
15 idle seconds, `: heartbeat` followed by a blank line appears. Keep it open
over 40 seconds to verify continued operation beyond the ordinary 30-second
HTTP write timeout. Use Ctrl+C to disconnect only this monitor.

In Postman use GET `{{API_BASE_URL}}/events`, Bearer `{{SESSION_TOKEN}}`, exactly
one `Accept: text/event-stream`, Body none and no query. Leave Last-Event-ID
absent initially. See the shared reference below for errors and exact frames.

To observe execution, leave the stream open and use another Postman tab to
compile [the H4 graph](../h4/compile-workflow.json) and start it with valid IR,
a fresh request ID, a READY device and `delayMs: 1000`. Existing device/ADB
configuration still applies to start admission. Expect node RUNNING/SUCCEEDED
events, per-device positions and a terminal group event. Match correlation IDs
to the POST response; other runs' events can appear on this Runtime-wide feed.

Save an event cursor after processing its data. Disconnect, start another run,
then reconnect with `Last-Event-ID` set to that positive cursor string. Curl
accepts an additional `-H "Last-Event-ID: 42"`, replacing 42 with your real value.
Later retained events replay before new events. Replaying does not execute nodes.

Missing Bearer returns401; missing Accept, malformed/zero/future cursor or query
returns400. Expired history returns410 before streaming. A failure after headers
closes the stream without appending JSON. Get an authoritative run snapshot and
reconnect after its cursor when repairing expiry. Owner shutdown closes streams;
a fresh process requires fresh readiness/authentication before reconnecting.

See the [complete shared SSE reference](../../../a2matex-plan/components/a2matex-runtime/integration/managed_runtime.md#execution-event-stream--i4)
and [Studio integration](../../../a2matex-plan/components/a2matex-runtime/integration/studio.md#live-execution-events--i4).
Linux automated tests use real HTTP/SQLite and controlled sinks, including an
actual-agent heartbeat/owner check. Native Windows and Studio UI acceptance are
separate from Windows cross-compilation. The existing missing G22/H14/H15
acceptance scripts remain an unrelated full-suite gate.
