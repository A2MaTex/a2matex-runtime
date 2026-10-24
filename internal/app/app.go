package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	httptransport "github.com/A2MaTex/a2matex-runtime/internal/transport/http"
)

type lifecycle interface {
	Start(ctx context.Context) error
	Wait() error
	Shutdown(ctx context.Context) error
}

type component struct {
	name      string
	lifecycle lifecycle
}

type componentResult struct {
	index int
	err   error
}

type App struct {
	components      []component
	shutdownTimeout time.Duration
	logger          *slog.Logger
}

func New(server *httptransport.Server, cfg config.Config, logger *slog.Logger) *App {
	return &App{
		components: []component{
			{name: "local http server", lifecycle: server},
		},
		shutdownTimeout: cfg.ShutdownTimeout,
		logger:          logger,
	}
}

func (a *App) Run(ctx context.Context) error {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	results := make(chan componentResult, len(a.components))
	started := make([]component, 0, len(a.components))

	a.logger.Info("starting runtime agent")

	for index, current := range a.components {
		if err := current.lifecycle.Start(runCtx); err != nil {
			cancelRun()

			var startErr error
			if ctx.Err() == nil || !errors.Is(err, context.Canceled) {
				startErr = fmt.Errorf("start %s: %w", current.name, err)
			}

			return errors.Join(startErr, a.cleanup(runCtx, started, results, nil))
		}

		started = append(started, current)
		go waitForComponent(index, current, results)
	}

	a.logger.Info("runtime agent started")

	observed := make(map[int]struct{}, len(started))
	var runErr error

	select {
	case <-ctx.Done():
	case result := <-results:
		observed[result.index] = struct{}{}
		if result.err != nil {
			runErr = fmt.Errorf("%s stopped: %w", started[result.index].name, result.err)
		} else if ctx.Err() == nil {
			runErr = fmt.Errorf("%s stopped unexpectedly", started[result.index].name)
		}
	}

	cancelRun()
	cleanupErr := a.cleanup(runCtx, started, results, observed)
	if err := errors.Join(runErr, cleanupErr); err != nil {
		return err
	}

	a.logger.Info("runtime agent stopped")

	return nil
}

func waitForComponent(index int, current component, results chan<- componentResult) {
	results <- componentResult{
		index: index,
		err:   current.lifecycle.Wait(),
	}
}

func (a *App) cleanup(
	runCtx context.Context,
	started []component,
	results <-chan componentResult,
	observed map[int]struct{},
) error {
	if observed == nil {
		observed = make(map[int]struct{}, len(started))
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(
		context.WithoutCancel(runCtx),
		a.shutdownTimeout,
	)
	defer cancelShutdown()

	var cleanupErrors []error
	for index := len(started) - 1; index >= 0; index-- {
		current := started[index]
		if err := current.lifecycle.Shutdown(shutdownCtx); err != nil {
			cleanupErrors = append(
				cleanupErrors,
				fmt.Errorf("shutdown %s: %w", current.name, err),
			)
		}
	}

	remaining := len(started) - len(observed)
	for remaining > 0 {
		select {
		case result := <-results:
			if _, exists := observed[result.index]; exists {
				continue
			}

			observed[result.index] = struct{}{}
			remaining--
			if result.err != nil {
				cleanupErrors = append(
					cleanupErrors,
					fmt.Errorf("wait for %s: %w", started[result.index].name, result.err),
				)
			}
		case <-shutdownCtx.Done():
			cleanupErrors = append(
				cleanupErrors,
				fmt.Errorf("wait for components: %w", shutdownCtx.Err()),
			)
			return errors.Join(cleanupErrors...)
		}
	}

	return errors.Join(cleanupErrors...)
}
