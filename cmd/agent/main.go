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
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
)

const exitFailure = 1

func main() {
	os.Exit(run())
}

func run() int {
	var overrides config.Overrides
	var migrateUp bool

	flag.StringVar(&overrides.ConfigFile, "config", "", "path to the optional configuration file")
	flag.StringVar(&overrides.ListenAddress, "listen-address", "", "loopback HTTP listen address")
	flag.StringVar(&overrides.LogLevel, "log-level", "", "log level: debug, info, warn, or error")
	flag.BoolVar(&migrateUp, "migrate-up", false, "apply pending SQLite migrations and exit")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if migrateUp {
		report, err := sqlitestore.MigrateUp(ctx)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "migrate execution database: %v\n", err)
			return exitFailure
		}
		for _, migration := range report.Applied {
			fmt.Printf("Applied %s (version %d, %s)\n", migration.Filename, migration.Version, migration.Duration)
		}
		if len(report.Applied) == 0 {
			fmt.Println("No pending migrations.")
		}
		fmt.Printf("Current database version: %d\n", report.Version)
		return 0
	}

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
