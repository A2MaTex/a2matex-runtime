package services

import (
	"context"
	"errors"
	"log/slog"
	"runtime"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// EventRetention performs bounded maintenance outside execution checkpoints.
type EventRetention struct {
	repository execution.EventRetentionRepository
	logger     *slog.Logger
	cancel     context.CancelFunc
	done       chan struct{}
}

func NewEventRetention(repository execution.EventRetentionRepository, logger *slog.Logger) *EventRetention {
	return &EventRetention{
		repository: repository,
		logger:     logger, done: make(chan struct{}),
	}
}

func (s *EventRetention) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	go s.run(runCtx)
	return nil
}

func (s *EventRetention) run(ctx context.Context) {
	defer close(s.done)
	for {
		// Run once on startup, then continue bounded batches while behind.
		removed, err := s.repository.PruneEvents(ctx, constants.EventRetention, constants.EventPruneBatchSize)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.logPruneFailure(ctx, err)
		}
		if err == nil && removed == constants.EventPruneBatchSize {
			runtime.Gosched()
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.repository.EventMaintenance():
		}
	}
}

func (s *EventRetention) logPruneFailure(ctx context.Context, err error) {
	failure := xerror.WithOperation("prune_execution_events", err)
	var operation *xerror.OperationError
	errors.As(failure, &operation)
	attrs := []slog.Attr{
		slog.String("component", "event_retention"),
		slog.String("event", "event_prune_failed"),
		slog.String("operation", operation.Operation),
	}
	if operation.Detail != "" {
		attrs = append(attrs, slog.String("reason", operation.Detail))
	}
	if code := xerror.ExecutionFailureCode(err); code != "" {
		attrs = append(attrs, slog.String("error_code", code))
	}
	s.logger.LogAttrs(ctx, slog.LevelError, "execution event retention failed", attrs...)
}

func (s *EventRetention) Wait() error {
	<-s.done
	return nil
}

func (s *EventRetention) Shutdown(ctx context.Context) error {
	if s.cancel == nil {
		return nil
	}
	s.cancel()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
