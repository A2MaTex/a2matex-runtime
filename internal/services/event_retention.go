package services

import (
	"context"
	"log/slog"
	"runtime"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
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
			s.logger.Error("execution event retention failed", "component", "event_retention", "event", "event_prune_failed")
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
