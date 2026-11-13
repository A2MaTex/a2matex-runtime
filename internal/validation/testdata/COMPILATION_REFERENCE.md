# Compilation fixture reference

`compilation-v1.json` records actual results from Backend
`WorkflowCompilerService.compileWorkflow`. It covers target mapping and
defaults, explicit timeout and partial retry overrides, an undeclared workflow
input, an invalid input schema, and an invalid node retry. The Go test compares
all validation fields and the full JSON value of the IR, including array order.

The installed `../nodecontract/catalog_v1.json` projects
`BASIC_NODE_DEFINITIONS` from Backend commit
`8d21b3218f158ad5266a2870fbc53e119e2f57d9` plus the staged
`feat/node-binding-kind` definition changes. The staged diff for
`src/routes/node-catalog/definitions` had SHA-256
`4465ebcd943f2a9b75af4d1595669dc5eacaddaaae81c29d0d7b6b4832978d2d`.
When the Backend definition semantics change, update the installed projection
and parity fixtures together and ship a corresponding Runtime build.
