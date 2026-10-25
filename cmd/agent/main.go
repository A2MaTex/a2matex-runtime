package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/config"
)

const exitFailure = 1

func main() {
	os.Exit(run())
}

func run() int {
	var overrides config.Overrides

	flag.StringVar(&overrides.ConfigFile, "config", "", "path to the optional configuration file")
	flag.StringVar(&overrides.ListenAddress, "listen-address", "", "loopback HTTP listen address")
	flag.StringVar(&overrides.LogLevel, "log-level", "", "log level: debug, info, warn, or error")
	flag.Parse()

	cfg, err := config.Load(overrides)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "load configuration: %v\n", err)
		return exitFailure
	}

	info := buildinfo.Current()
	application, err := initializeApp(cfg, info)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "initialize runtime agent: %v\n", err)
		return exitFailure
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := application.Run(ctx); err != nil {
		slog.Error("runtime agent stopped with an error",
			"component", "runtime",
			"event", "runtime_failed",
			"error", err,
		)
		return exitFailure
	}

	return 0
}
