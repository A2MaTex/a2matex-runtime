package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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
	exitInvalidFlags = 2
)

func main() {
	os.Exit(run())
}

func run() int {
	var overrides config.Overrides
	var migrateUp bool
	var ownerCommands, ownerReady uint64

	startupLogger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(startupLogger)
	flags := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&overrides.ConfigFile, "config", "", "path to the optional configuration file")
	flags.StringVar(&overrides.ListenAddress, "listen-address", "", "loopback HTTP listen address")
	flags.StringVar(&overrides.LogLevel, "log-level", "", "log level: debug, info, warn, or error")
	flags.BoolVar(&migrateUp, "migrate-up", false, "apply pending SQLite migrations and exit")
	flags.Uint64Var(&ownerCommands, "owner-command-pipe", 0, "inherited command pipe handle")
	flags.Uint64Var(&ownerReady, "owner-ready-pipe", 0, "inherited readiness pipe handle")
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(os.Stderr)
			flags.Usage()
			return 0
		}
		logRuntimeFailure(context.Background(), startupLogger, "parse_launch_flags", "invalid Runtime command-line flags", err)
		return exitInvalidFlags
	}

	var managed bool
	flags.Visit(func(option *flag.Flag) {
		if option.Name == "owner-command-pipe" || option.Name == "owner-ready-pipe" {
			managed = true
		}
	})
	if managed && (migrateUp || uint64(uintptr(ownerCommands)) != ownerCommands || uint64(uintptr(ownerReady)) != ownerReady) {
		logRuntimeFailure(context.Background(), startupLogger, "validate_managed_launch", "invalid managed Runtime launch options", nil)
		return exitFailure
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if migrateUp {
		report, err := sqlitestore.MigrateUp(ctx)
		if err != nil {
			logRuntimeFailure(ctx, startupLogger, "migrate_up", "could not migrate the execution database", err)
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
		logRuntimeFailure(ctx, startupLogger, "load_config", "could not load Runtime configuration", err)
		return exitFailure
	}

	var owner *discovery.Owner
	if managed {
		owner, err = discovery.OpenOwner(uintptr(ownerCommands), uintptr(ownerReady))
		if err != nil {
			logRuntimeFailure(ctx, startupLogger, "open_owner_pipes", "managed launch requires two valid, distinct inherited pipes", err)
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
		logRuntimeFailure(ctx, startupLogger, "initialize_runtime", "could not initialize the Runtime agent", err)
		return exitFailure
	}

	if err := application.Run(ctx); err != nil {
		logRuntimeFailure(ctx, slog.Default(), "run_runtime", "runtime agent stopped with an error", err)
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
