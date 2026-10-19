# A2MaTeX Runtime Agent

Local Go agent responsible for executing approved A2MaTeX workflows against selected Android devices.

This initial scaffold contains only the process foundation:

- typed configuration with Viper;
- structured logging with `log/slog`;
- compile-time dependency wiring with Google Wire;
- graceful shutdown;
- a loopback-only Gin HTTP server;
- health and runtime-information endpoints.

Device discovery, workflow execution, persistence, local authentication, and node handlers are intentionally not implemented yet.

## Requirements

- Go 1.25 or newer.
- Wire when regenerating dependency wiring.
- golangci-lint when running lint checks.

## Configuration

Copy `config/config.example.yaml` to `config/config.yaml` when local overrides are needed. The file is optional.

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
-config config/config.yaml
-listen-address 127.0.0.1:9000
-log-level debug
```

The HTTP listener rejects non-loopback addresses.

## Commands

```bash
make wire
make fmt
make build
make run
make lint
```

Initial endpoints:

```text
GET /api/v1/health
GET /api/v1/runtime-info
```

These endpoints are currently unauthenticated and reachable only through the configured loopback listener. Authentication will be added with the approved local API contract.
