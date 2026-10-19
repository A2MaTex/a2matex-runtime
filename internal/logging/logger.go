package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"a2matex-runtime/internal/config"
)

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
	options := &slog.HandlerOptions{Level: level}
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
