package services

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

type retentionStub struct {
	signal chan struct{}
	calls  chan struct{}
	prune  func(context.Context, int, int) (int, error)
}

func (s *retentionStub) PruneEvents(ctx context.Context, retain, batch int) (int, error) {
	removed, err := s.prune(ctx, retain, batch)
	select {
	case s.calls <- struct{}{}:
	case <-ctx.Done():
	}
	return removed, err
}

func (s *retentionStub) EventMaintenance() <-chan struct{} { return s.signal }

func awaitPrune(t *testing.T, calls <-chan struct{}) {
	t.Helper()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("retention did not run")
	}
}

func TestEventRetentionStartupBatchesAndCommitSignal(t *testing.T) {
	count := 0
	repo := &retentionStub{signal: make(chan struct{}, 1), calls: make(chan struct{}, 8)}
	repo.prune = func(_ context.Context, retain, batch int) (int, error) {
		if retain != constants.EventRetention || batch != constants.EventPruneBatchSize {
			return 0, errors.New("unexpected retention limits")
		}
		count++
		if count <= 2 {
			return batch, nil
		}
		return 0, nil
	}
	service := NewEventRetention(repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := service.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	for range 3 {
		awaitPrune(t, repo.calls)
	}
	repo.signal <- struct{}{}
	awaitPrune(t, repo.calls)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestEventRetentionFailureWaitsForNextCommit(t *testing.T) {
	repo := &retentionStub{signal: make(chan struct{}, 1), calls: make(chan struct{}, 8),
		prune: func(context.Context, int, int) (int, error) { return 0, errors.New("storage failed") }}
	service := NewEventRetention(repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := service.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	awaitPrune(t, repo.calls)
	repo.signal <- struct{}{}
	awaitPrune(t, repo.calls)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
