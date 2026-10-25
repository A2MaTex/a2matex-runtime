package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
)

const redactedValue = "[REDACTED]"

var sensitiveAttributeKeys = map[string]struct{}{
	"adbserial":      {},
	"artifactpath":   {},
	"authorization":  {},
	"cookie":         {},
	"credential":     {},
	"credentials":    {},
	"filepath":       {},
	"password":       {},
	"payload":        {},
	"requestbody":    {},
	"responsebody":   {},
	"runtimeir":      {},
	"secret":         {},
	"sessiontoken":   {},
	"setcookie":      {},
	"stderr":         {},
	"stdout":         {},
	"token":          {},
	"workflowinput":  {},
	"workflowoutput": {},
}

func New(cfg config.Config) (*slog.Logger, error) {
	level, err := parseLevel(cfg.Logging.Level)
	if err != nil {
		return nil, err
	}

	logger := slog.New(newHandler(os.Stdout, cfg.Logging.Format, level))
	slog.SetDefault(logger)

	return logger, nil
}

func newHandler(output io.Writer, format string, level slog.Level) slog.Handler {
	options := &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: redactAttribute,
	}
	if strings.EqualFold(format, "text") {
		return slog.NewTextHandler(output, options)
	}

	return slog.NewJSONHandler(output, options)
}

func parseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unsupported log level %q", value)
	}
}

func redactAttribute(_ []string, attribute slog.Attr) slog.Attr {
	if _, sensitive := sensitiveAttributeKeys[normalizeAttributeKey(attribute.Key)]; sensitive {
		return slog.String(attribute.Key, redactedValue)
	}

	return attribute
}

func normalizeAttributeKey(key string) string {
	return strings.ReplaceAll(strings.ToLower(key), "_", "")
}
