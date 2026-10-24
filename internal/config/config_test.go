package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	clearEnvironment(t)

	got, err := Load(Overrides{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := validConfig()
	if got != want {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestLoadPrecedence(t *testing.T) {
	clearEnvironment(t)
	t.Setenv("A2MATEX_RUNTIME_HTTP_READ_TIMEOUT", "15s")
	t.Setenv("A2MATEX_RUNTIME_HTTP_LISTEN_ADDRESS", "127.0.0.1:9001")
	t.Setenv("A2MATEX_RUNTIME_LOGGING_LEVEL", "WARN")

	configFile := writeConfigFile(t, `
shutdown_timeout: 20s
http:
  listen_address: 127.0.0.1:9000
  write_timeout: 45s
logging:
  level: info
  format: text
`)

	got, err := Load(Overrides{
		ConfigFile:    configFile,
		ListenAddress: "127.0.0.1:9002",
		LogLevel:      "ERROR",
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got.ShutdownTimeout != 20*time.Second {
		t.Errorf("ShutdownTimeout = %s, want 20s", got.ShutdownTimeout)
	}
	if got.HTTP.ListenAddress != "127.0.0.1:9002" {
		t.Errorf("HTTP.ListenAddress = %q, want command-line override", got.HTTP.ListenAddress)
	}
	if got.HTTP.ReadTimeout != 15*time.Second {
		t.Errorf("HTTP.ReadTimeout = %s, want environment override", got.HTTP.ReadTimeout)
	}
	if got.HTTP.WriteTimeout != 45*time.Second {
		t.Errorf("HTTP.WriteTimeout = %s, want file value", got.HTTP.WriteTimeout)
	}
	if got.HTTP.IdleTimeout != defaultIdleTimeout {
		t.Errorf("HTTP.IdleTimeout = %s, want default %s", got.HTTP.IdleTimeout, defaultIdleTimeout)
	}
	if got.Logging.Level != "error" {
		t.Errorf("Logging.Level = %q, want normalized command-line override", got.Logging.Level)
	}
	if got.Logging.Format != "text" {
		t.Errorf("Logging.Format = %q, want file value", got.Logging.Format)
	}
}

func TestLoadRejectsInvalidFile(t *testing.T) {
	clearEnvironment(t)

	tests := []struct {
		name        string
		content     string
		wantErrPart string
	}{
		{
			name: "unknown key",
			content: `
shutdown_timeout: 10s
unknown_setting: true
`,
			wantErrPart: "decode configuration",
		},
		{
			name: "removed mode key",
			content: `
mode: production
`,
			wantErrPart: "decode configuration",
		},
		{
			name:        "malformed yaml",
			content:     "http: [",
			wantErrPart: "read configuration file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(Overrides{ConfigFile: writeConfigFile(t, tt.content)})
			if err == nil || !strings.Contains(err.Error(), tt.wantErrPart) {
				t.Fatalf("Load() error = %v, want error containing %q", err, tt.wantErrPart)
			}
		})
	}
}

func TestLoadRejectsMissingExplicitFile(t *testing.T) {
	clearEnvironment(t)

	_, err := Load(Overrides{ConfigFile: filepath.Join(t.TempDir(), "missing.yaml")})
	if err == nil || !strings.Contains(err.Error(), "read configuration file") {
		t.Fatalf("Load() error = %v, want explicit file error", err)
	}
}

func TestLoadNormalizesSupportedLoggingValues(t *testing.T) {
	tests := []struct {
		name       string
		level      string
		format     string
		wantLevel  string
		wantFormat string
	}{
		{name: "debug JSON", level: "debug", format: "JSON", wantLevel: "debug", wantFormat: "json"},
		{name: "info text", level: "info", format: "text", wantLevel: "info", wantFormat: "text"},
		{name: "warn JSON", level: "WARN", format: "json", wantLevel: "warn", wantFormat: "json"},
		{name: "error text", level: "ERROR", format: "TEXT", wantLevel: "error", wantFormat: "text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnvironment(t)
			t.Setenv("A2MATEX_RUNTIME_LOGGING_LEVEL", tt.level)
			t.Setenv("A2MATEX_RUNTIME_LOGGING_FORMAT", tt.format)

			cfg, err := Load(Overrides{})
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Logging.Level != tt.wantLevel {
				t.Errorf("Logging.Level = %q, want %q", cfg.Logging.Level, tt.wantLevel)
			}
			if cfg.Logging.Format != tt.wantFormat {
				t.Errorf("Logging.Format = %q, want %q", cfg.Logging.Format, tt.wantFormat)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*Config)
		wantErrPart string
	}{
		{
			name: "IPv6 loopback",
			mutate: func(cfg *Config) {
				cfg.HTTP.ListenAddress = "[::1]:8765"
			},
		},
		{
			name: "minimum durations",
			mutate: func(cfg *Config) {
				cfg.ShutdownTimeout = minShutdownTimeout
				cfg.HTTP.ReadHeaderTimeout = minHTTPTimeout
				cfg.HTTP.ReadTimeout = minHTTPTimeout
				cfg.HTTP.WriteTimeout = minHTTPTimeout
				cfg.HTTP.IdleTimeout = minHTTPTimeout
			},
		},
		{
			name: "maximum durations",
			mutate: func(cfg *Config) {
				cfg.ShutdownTimeout = maxShutdownTimeout
				cfg.HTTP.ReadHeaderTimeout = maxReadHeaderTimeout
				cfg.HTTP.ReadTimeout = maxReadTimeout
				cfg.HTTP.WriteTimeout = maxWriteTimeout
				cfg.HTTP.IdleTimeout = maxIdleTimeout
			},
		},
		{
			name: "wildcard address",
			mutate: func(cfg *Config) {
				cfg.HTTP.ListenAddress = "0.0.0.0:8765"
			},
			wantErrPart: "must use a loopback IP",
		},
		{
			name: "hostname",
			mutate: func(cfg *Config) {
				cfg.HTTP.ListenAddress = "localhost:8765"
			},
			wantErrPart: "must be an IP address and port",
		},
		{
			name: "zero port",
			mutate: func(cfg *Config) {
				cfg.HTTP.ListenAddress = "127.0.0.1:0"
			},
			wantErrPart: "must use a non-zero port",
		},
		{
			name: "shutdown below minimum",
			mutate: func(cfg *Config) {
				cfg.ShutdownTimeout = minShutdownTimeout - time.Nanosecond
			},
			wantErrPart: "shutdown_timeout must be between",
		},
		{
			name: "shutdown above maximum",
			mutate: func(cfg *Config) {
				cfg.ShutdownTimeout = maxShutdownTimeout + time.Nanosecond
			},
			wantErrPart: "shutdown_timeout must be between",
		},
		{
			name: "read header above maximum",
			mutate: func(cfg *Config) {
				cfg.HTTP.ReadHeaderTimeout = maxReadHeaderTimeout + time.Nanosecond
			},
			wantErrPart: "http.read_header_timeout must be between",
		},
		{
			name: "read above maximum",
			mutate: func(cfg *Config) {
				cfg.HTTP.ReadTimeout = maxReadTimeout + time.Nanosecond
			},
			wantErrPart: "http.read_timeout must be between",
		},
		{
			name: "write above maximum",
			mutate: func(cfg *Config) {
				cfg.HTTP.WriteTimeout = maxWriteTimeout + time.Nanosecond
			},
			wantErrPart: "http.write_timeout must be between",
		},
		{
			name: "idle above maximum",
			mutate: func(cfg *Config) {
				cfg.HTTP.IdleTimeout = maxIdleTimeout + time.Nanosecond
			},
			wantErrPart: "http.idle_timeout must be between",
		},
		{
			name: "invalid log level",
			mutate: func(cfg *Config) {
				cfg.Logging.Level = "verbose"
			},
			wantErrPart: "logging.level must be",
		},
		{
			name: "invalid log format",
			mutate: func(cfg *Config) {
				cfg.Logging.Format = "xml"
			},
			wantErrPart: "logging.format must be",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)

			err := cfg.Validate()
			if tt.wantErrPart == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErrPart) {
				t.Fatalf("Validate() error = %v, want error containing %q", err, tt.wantErrPart)
			}
		})
	}
}

func validConfig() Config {
	return Config{
		ShutdownTimeout: defaultShutdownTimeout,
		HTTP: HTTPConfig{
			ListenAddress:     defaultListenAddress,
			ReadHeaderTimeout: defaultReadHeaderTimeout,
			ReadTimeout:       defaultReadTimeout,
			WriteTimeout:      defaultWriteTimeout,
			IdleTimeout:       defaultIdleTimeout,
		},
		Logging: LoggingConfig{
			Level:  defaultLogLevel,
			Format: defaultLogFormat,
		},
	}
}

func clearEnvironment(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"A2MATEX_RUNTIME_SHUTDOWN_TIMEOUT",
		"A2MATEX_RUNTIME_HTTP_LISTEN_ADDRESS",
		"A2MATEX_RUNTIME_HTTP_READ_HEADER_TIMEOUT",
		"A2MATEX_RUNTIME_HTTP_READ_TIMEOUT",
		"A2MATEX_RUNTIME_HTTP_WRITE_TIMEOUT",
		"A2MATEX_RUNTIME_HTTP_IDLE_TIMEOUT",
		"A2MATEX_RUNTIME_LOGGING_LEVEL",
		"A2MATEX_RUNTIME_LOGGING_FORMAT",
	} {
		t.Setenv(key, "")
	}
}

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	return path
}
