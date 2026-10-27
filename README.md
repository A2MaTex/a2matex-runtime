# A2MaTeX Runtime Agent

Local Go agent responsible for executing approved A2MaTeX workflows against selected Android devices.

The current codebase is the initial process foundation:

- typed configuration with Viper;
- structured logging with `log/slog`, central key-based redaction, and safe
  lifecycle and Local API records;
- compile-time dependency wiring with Google Wire;
- supervisor-owned startup, failure monitoring, and bounded graceful shutdown;
- a loopback-only Gin HTTP server;
- per-start local session authentication material and protected connection-file
  publication;
- public health and authenticated runtime-information endpoints.

B1 establishes the buildable Go project foundation, B2 provides validated
startup configuration, B3 coordinates the lifecycle of the currently
implemented HTTP component, and B4 protects operational logs through stable
fields, route-pattern request records, and redaction. B5 creates the local
session verifier and publishes its connection handoff after listener readiness,
and B6 applies that verifier to the Local API compatibility handshake. Device
discovery, workflow execution, persistence, and node handlers belong to later
phases and are not implemented yet.

## Requirements

- Go 1.25 or newer.
- Wire when regenerating dependency wiring.
- golangci-lint when running lint checks.

## Configuration

Runtime uses built-in defaults when no configuration file is supplied. To use local overrides, copy `configs/config.example.yaml` to `configs/config.yaml` and select it explicitly with `-config configs/config.yaml`.

Configuration precedence is:

1. command-line overrides;
2. `A2MATEX_RUNTIME_*` environment variables;
3. explicitly selected configuration file;
4. built-in defaults.

Nested environment keys use underscores. For example:

```text
A2MATEX_RUNTIME_HTTP_LISTEN_ADDRESS=127.0.0.1:9000
A2MATEX_RUNTIME_LOGGING_LEVEL=debug
```

Supported command-line overrides:

```text
-config configs/config.yaml
-listen-address 127.0.0.1:9000
-log-level debug
```

The HTTP listener rejects non-loopback addresses.
Unknown configuration-file keys and invalid values stop startup before the HTTP server is created. Configuration is loaded once; changing the file requires restarting Runtime.

## Commands

```bash
make wire
make fmt
make test
make build
make run
make lint
```

Local API foundation endpoints:

```text
GET /api/v1/health
GET /api/v1/runtime-info
```

`GET /api/v1/health` is the only unauthenticated endpoint and returns only the
process-liveness status. Every other request requires the per-start bearer token
published in `runtime-connection.json`. The authenticated Runtime-information
response exposes only the Runtime version, accepted Runtime IR format versions,
and currently implemented node handlers. Every non-health response uses
`Cache-Control: no-store`.
