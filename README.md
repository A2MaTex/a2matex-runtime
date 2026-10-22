# A2MaTeX Runtime Agent

Local Go agent responsible for executing approved A2MaTeX workflows against selected Android devices.

The current codebase is the initial process scaffold:

- typed configuration with Viper;
- structured logging with `log/slog`;
- compile-time dependency wiring with Google Wire;
- graceful shutdown;
- a loopback-only Gin HTTP server;
- health and runtime-information endpoints.

B1 establishes the buildable Go project foundation. Device discovery, workflow execution, persistence, local authentication, and node handlers belong to later phases and are not implemented yet.

## Requirements

- Go 1.25 or newer.
- Wire when regenerating dependency wiring.
- golangci-lint when running lint checks.

## Configuration

Copy `configs/config.example.yaml` to `configs/config.yaml` when local overrides are needed. The file is optional.

Configuration precedence is:

1. command-line overrides;
2. `A2MATEX_RUNTIME_*` environment variables;
3. configuration file;
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

## Commands

```bash
make wire
make fmt
make test
make build
make run
make lint
```

Preliminary scaffold endpoints:

```text
GET /api/v1/health
GET /api/v1/runtime-info
```

These handlers are not yet compliant with the approved A7 local API contract. B5 and B6 will add session authentication and align the routes and payloads with that contract. Until then, the preliminary endpoints remain unauthenticated and are reachable only through the configured loopback listener.
