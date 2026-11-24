These two files are unchanged copies of the approved A8 v2 fixtures:

- `a2matex-plan/components/a2matex-runtime/contracts/fixtures/v2/local_api/valid_node_attempt_event.sse`
- `a2matex-plan/components/a2matex-runtime/contracts/fixtures/v2/event_error/valid_node_attempt_event.json`

`TestWriterFramesAndA8Conformance` compares the encoded frame byte-for-byte and
uses the JSON event as its input. Runtime tests do not require a sibling Plan
checkout. Do not edit frozen fixture content to match a transport change.

The local `.gitattributes` preserves LF fixture bytes on Windows checkouts.
The SSE file intentionally ends with a blank line to terminate the frame; only
that protocol-required blank-at-EOF check is exempted for `.sse` files.
