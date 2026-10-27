package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/discovery"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/httpserver"
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

func New(
	server *httpserver.Server,
	publisher *discovery.Publisher,
	cfg config.Config,
	logger *slog.Logger,
) *App {
	return &App{
		// Components start in this order and stop in reverse. This guarantees the
		// connection file appears only after bind and disappears before HTTP stop.
		components: []component{
			{name: "local_http_server", lifecycle: server},
			{name: "connection_file_publisher", lifecycle: publisher},
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

	a.logger.Info("starting runtime agent",
		"component", "runtime",
		"event", "runtime_starting",
	)

	for index, current := range a.components {
		if result, stopped := pollComponentResult(results); stopped {
			return a.stopAfterComponentResult(ctx, runCtx, cancelRun, started, results, result)
		}

		if err := current.lifecycle.Start(runCtx); err != nil {
			cancelRun()

			if result, stopped := pollComponentResult(results); stopped {
				var startErr error
				if !errors.Is(err, context.Canceled) {
					startErr = fmt.Errorf("start %s: %w", current.name, err)
				}

				return errors.Join(
					componentStopError(ctx, started, result),
					startErr,
					a.cleanup(runCtx, started, results, observedResult(result)),
				)
			}

			return errors.Join(
				componentStartError(ctx, current, err),
				a.cleanup(runCtx, started, results, nil),
			)
		}

		started = append(started, current)
		go waitForComponent(index, current, results, cancelRun)
	}

	if result, stopped := pollComponentResult(results); stopped {
		return a.stopAfterComponentResult(ctx, runCtx, cancelRun, started, results, result)
	}
	if runCtx.Err() != nil {
		// A watcher publishes before cancelling runCtx, so a cancellation not
		// caused by the parent must have a terminal result available now.
		if result, stopped := pollComponentResult(results); stopped {
			return a.stopAfterComponentResult(ctx, runCtx, cancelRun, started, results, result)
		}

		cancelRun()
		return a.cleanup(runCtx, started, results, nil)
	}

	a.logger.Info("runtime agent ready",
		"component", "runtime",
		"event", "runtime_ready",
	)

	observed := make(map[int]struct{}, len(started))
	var runErr error

	select {
	case <-ctx.Done():
	case result := <-results:
		observed[result.index] = struct{}{}
		runErr = componentStopError(ctx, started, result)
	}

	cancelRun()
	cleanupErr := a.cleanup(runCtx, started, results, observed)
	if err := errors.Join(runErr, cleanupErr); err != nil {
		return err
	}

	a.logger.Info("runtime agent stopped",
		"component", "runtime",
		"event", "runtime_stopped",
	)

	return nil
}

func waitForComponent(
	index int,
	current component,
	results chan<- componentResult,
	cancelRun context.CancelFunc,
) {
	results <- componentResult{
		index: index,
		err:   current.lifecycle.Wait(),
	}
	// Publishing first preserves the component's terminal result as the reason
	// startup was cancelled instead of reporting a secondary context error.
	cancelRun()
}

func pollComponentResult(results <-chan componentResult) (componentResult, bool) {
	select {
	case result := <-results:
		return result, true
	default:
		return componentResult{}, false
	}
}

func observedResult(result componentResult) map[int]struct{} {
	return map[int]struct{}{result.index: {}}
}

func componentStartError(parentCtx context.Context, current component, err error) error {
	if parentCtx.Err() != nil && errors.Is(err, context.Canceled) {
		return nil
	}

	return fmt.Errorf("start %s: %w", current.name, err)
}

func componentStopError(
	parentCtx context.Context,
	started []component,
	result componentResult,
) error {
	if parentErr := parentCtx.Err(); parentErr != nil && errors.Is(result.err, parentErr) {
		return nil
	}
	if result.err != nil {
		return fmt.Errorf("%s stopped: %w", started[result.index].name, result.err)
	}
	if parentCtx.Err() == nil {
		return fmt.Errorf("%s stopped unexpectedly", started[result.index].name)
	}

	return nil
}

func (a *App) stopAfterComponentResult(
	parentCtx context.Context,
	runCtx context.Context,
	cancelRun context.CancelFunc,
	started []component,
	results <-chan componentResult,
	result componentResult,
) error {
	cancelRun()

	return errors.Join(
		componentStopError(parentCtx, started, result),
		a.cleanup(runCtx, started, results, observedResult(result)),
	)
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
		// Prefer results that are already available. Without this drain, a select
		// may choose an expired deadline while completed watcher results remain
		// queued, incorrectly reporting that they were not joined.
		if result, completed := pollComponentResult(results); completed {
			var waitErr error
			remaining, waitErr = collectComponentResult(started, observed, result, remaining)
			if waitErr != nil {
				cleanupErrors = append(cleanupErrors, waitErr)
			}
			continue
		}

		select {
		case result := <-results:
			var waitErr error
			remaining, waitErr = collectComponentResult(started, observed, result, remaining)
			if waitErr != nil {
				cleanupErrors = append(cleanupErrors, waitErr)
			}
		case <-shutdownCtx.Done():
			// A watcher may have published at the same time as the deadline. Drain
			// that result before declaring it unjoined.
			if result, completed := pollComponentResult(results); completed {
				var waitErr error
				remaining, waitErr = collectComponentResult(started, observed, result, remaining)
				if waitErr != nil {
					cleanupErrors = append(cleanupErrors, waitErr)
				}
				continue
			}

			cleanupErrors = append(
				cleanupErrors,
				fmt.Errorf("wait for components: %w", shutdownCtx.Err()),
			)
			return errors.Join(cleanupErrors...)
		}
	}

	return errors.Join(cleanupErrors...)
}

func collectComponentResult(
	started []component,
	observed map[int]struct{},
	result componentResult,
	remaining int,
) (int, error) {
	if _, exists := observed[result.index]; exists {
		return remaining, nil
	}

	observed[result.index] = struct{}{}
	remaining--
	if result.err != nil {
		return remaining, fmt.Errorf("wait for %s: %w", started[result.index].name, result.err)
	}

	return remaining, nil
}
