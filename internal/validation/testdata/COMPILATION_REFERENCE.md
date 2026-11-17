# Compilation fixture reference

`compilation-v1.json` records actual results from Backend
`WorkflowCompilerService.compileWorkflow`. It covers target mapping and
defaults, explicit timeout and partial retry overrides, an undeclared workflow
input, an invalid input schema, and an invalid node retry. The Go test compares
all validation fields and the full JSON value of the IR, including array order.

The original compilation fixtures used a projection of
`BASIC_NODE_DEFINITIONS` from Backend commit
`8d21b3218f158ad5266a2870fbc53e119e2f57d9` plus the staged
`feat/node-binding-kind` definition changes. The staged diff for
`src/routes/node-catalog/definitions` had SHA-256
`4465ebcd943f2a9b75af4d1595669dc5eacaddaaae81c29d0d7b6b4832978d2d`.
When the Backend definition semantics change, update the installed projection
and parity fixtures together and ship a corresponding Runtime build.

## G1 shared-contract refresh (2026-11-18)

The installed `../nodecontract/catalog_v1.json` now projects the current
`BASIC_NODE_DEFINITIONS` from Backend commit
`96a7b258fb47a55786f723bb68ce8d8a4bc2dd51`. The projection includes per-field
Set Variable/Switch binding kinds and Switch dynamic operand metadata.

The shared helpers now accept UUID Switch rule IDs, all 14 Backend operators,
finite decimal scalar bindings, and `number` workflow properties. Set Variable
requires explicit `FLOW` or `RUN_GROUP` scope, matching the Backend definition.

`binding-parity.json` replaces obsolete rule names consistently in configs,
bindings, edges, and expected paths. Assignment cases now supply explicit
`FLOW` where they previously assumed a default. All 36 complete expected
validation responses were checked directly against Backend
`EditorGraph.parse`, `validateEditorGraphTopology`,
`validateEditorGraphCatalog`, and `buildValidationOutput` using those current
definitions. Expected codes, messages, node IDs, paths, status, warnings, and
truncation match. The Backend checkout was not modified. Existing compilation,
targeting, port-count, and topology fixtures continue to pass unchanged.

G1 compiler-to-admission tests separately cover decimal workflow input assignment
and UUID Switch rules using test-only enabled callables. This does not enable
Set Variable or Switch execution in the shipped agent registry.
