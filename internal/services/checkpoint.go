package services

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// CheckpointService owns attempt transitions and reports fatal persistence
// failures to the supervisor through Wait.
type CheckpointService struct {
	repository execution.CheckpointRepository
	gate       chan struct{}
	done       chan struct{}
	stop       sync.Once
	failure    error
}

func NewCheckpointService(repository execution.CheckpointRepository) *CheckpointService {
	return &CheckpointService{repository: repository, gate: make(chan struct{}, 1), done: make(chan struct{})}
}

func (s *CheckpointService) Start(ctx context.Context) error { return ctx.Err() }

func (s *CheckpointService) Wait() error {
	<-s.done
	return s.failure
}

func (s *CheckpointService) Shutdown(ctx context.Context) error {
	s.stop.Do(func() { close(s.done) })
	select {
	case s.gate <- struct{}{}:
		<-s.gate
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *CheckpointService) stoppedError() error {
	if s.failure != nil {
		return s.failure
	}
	return xerror.ErrCheckpointStopped
}

type checkpointWrite struct {
	result  execution.CheckpointResult
	matches func(execution.CheckpointState) bool
}

func (s *CheckpointService) write(ctx context.Context, ref execution.AttemptRef, finishing bool,
	operation func(execution.CheckpointTx) (checkpointWrite, error),
) (execution.CheckpointResult, error) {
	// SQLite already serializes writes. Keep this gate through read-back so a
	// duplicate checkpoint cannot create the evidence for an uncertain commit.
	select {
	case <-s.done:
		return execution.CheckpointResult{}, s.stoppedError()
	case <-ctx.Done():
		return execution.CheckpointResult{}, s.checkpointError(finishing, ctx.Err())
	case s.gate <- struct{}{}:
	}
	defer func() { <-s.gate }()
	select {
	case <-s.done:
		return execution.CheckpointResult{}, s.stoppedError()
	default:
	}
	if err := ctx.Err(); err != nil {
		return execution.CheckpointResult{}, s.checkpointError(finishing, err)
	}
	var write checkpointWrite
	err := s.repository.WithCheckpointTx(ctx, func(tx execution.CheckpointTx) error {
		var err error
		write, err = operation(tx)
		return err
	})
	if err == nil {
		return s.result(write.result)
	}
	if errors.Is(err, xerror.ErrExecutionNotFound) || errors.Is(err, xerror.ErrExecutionStaleState) {
		return execution.CheckpointResult{Disposition: constants.CheckpointStale}, nil
	}
	if errors.Is(err, xerror.ErrExecutionCommitUnknown) {
		if write.matches == nil && write.result.Disposition == constants.CheckpointAlreadyFinalized {
			// The read-only duplicate path made no changes that require reconciliation.
			return write.result, nil
		}
		state, watermark, readErr := s.repository.ReadCheckpoint(ctx, ref)
		cursor, cursorErr := strconv.ParseInt(write.result.Cursor, 10, 64)
		if readErr == nil && cursorErr == nil && watermark >= cursor && write.matches != nil && write.matches(state) {
			s.repository.NotifyRecoveredCheckpoint()
			return s.result(write.result)
		}
		return execution.CheckpointResult{}, s.fail("reconcile attempt checkpoint", err)
	}
	return execution.CheckpointResult{}, s.checkpointError(finishing, err)
}

func (s *CheckpointService) result(result execution.CheckpointResult) (execution.CheckpointResult, error) {
	select {
	case <-s.done:
		return execution.CheckpointResult{}, s.stoppedError()
	default:
		return result, nil
	}
}

func (s *CheckpointService) checkpointError(finishing bool, err error) error {
	if finishing && !errors.Is(err, xerror.ErrExecutionInvalidInput) {
		return s.fail("finish attempt checkpoint", err)
	}
	return err
}

func (s *CheckpointService) fail(operation string, cause error) error {
	err := fmt.Errorf("%w: %s: %w", xerror.ErrCheckpointFatal, operation, cause)
	s.stop.Do(func() {
		s.failure = err
		close(s.done)
	})
	return err
}

func validAttemptRef(ref execution.AttemptRef) bool {
	return execution.ValidID(ref.RunGroupID) && execution.ValidID(ref.DeviceExecutionID) &&
		execution.ValidID(ref.AttemptID) && execution.ValidNodeID(ref.NodeID)
}

func sameAttempt(attempt *execution.CheckpointAttempt, ref execution.AttemptRef) bool {
	return attempt != nil && attempt.ID == ref.AttemptID && attempt.NodeID == ref.NodeID
}

func appendCheckpoint(ctx context.Context, tx execution.CheckpointTx, sequence int64,
	events []execution.PendingEvent, matches func(execution.CheckpointState) bool,
) (checkpointWrite, error) {
	cursors, err := tx.AppendEvents(ctx, events)
	if err != nil {
		return checkpointWrite{}, err
	}
	return checkpointWrite{
		result: execution.CheckpointResult{Disposition: constants.CheckpointCommitted,
			Sequence: sequence, Cursor: cursors[len(cursors)-1]},
		matches: matches,
	}, nil
}
