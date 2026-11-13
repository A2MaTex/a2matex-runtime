package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/discovery"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/supervisor"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const (
	exitFailure      = 1
	exitRuntimeInUse = 2
)

func main() {
	os.Exit(run())
}

func run() int {
	var overrides config.Overrides
	var migrateUp bool
	var ownerCommands, ownerReady uint64

	flag.StringVar(&overrides.ConfigFile, "config", "", "path to the optional configuration file")
	flag.StringVar(&overrides.ListenAddress, "listen-address", "", "loopback HTTP listen address")
	flag.StringVar(&overrides.LogLevel, "log-level", "", "log level: debug, info, warn, or error")
	flag.BoolVar(&migrateUp, "migrate-up", false, "apply pending SQLite migrations and exit")
	flag.Uint64Var(&ownerCommands, "owner-command-pipe", 0, "inherited command pipe handle")
	flag.Uint64Var(&ownerReady, "owner-ready-pipe", 0, "inherited readiness pipe handle")
	flag.Parse()

	var managed bool
	flag.Visit(func(option *flag.Flag) {
		if option.Name == "owner-command-pipe" || option.Name == "owner-ready-pipe" {
			managed = true
		}
	})
	if managed && (migrateUp || uint64(uintptr(ownerCommands)) != ownerCommands || uint64(uintptr(ownerReady)) != ownerReady) {
		_, _ = fmt.Fprintln(os.Stderr, "invalid managed Runtime launch options")
		return exitFailure
	}

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

	var owner *discovery.Owner
	if managed {
		owner, err = discovery.OpenOwner(uintptr(ownerCommands), uintptr(ownerReady))
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			return exitFailure
		}
		defer owner.Close()
		ownerCtx, cancelOwner := context.WithCancel(ctx)
		defer cancelOwner()
		go owner.Watch(cancelOwner)
		ctx = ownerCtx
	}

	application, err := initializeAppWithOwner(cfg, buildinfo.Current(), owner)
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
		if managed && errors.Is(err, xerror.ErrExecutionStoreInUse) {
			return exitRuntimeInUse
		}
		return exitFailure
	}

	return 0
}

func initializeApp(cfg config.Config, info buildinfo.Info) (*supervisor.App, error) {
	return initializeAppWithOwner(cfg, info, nil)
}
