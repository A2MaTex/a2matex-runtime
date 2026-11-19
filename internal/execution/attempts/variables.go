package attempts

import (
	"context"
	"errors"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outcomes"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/setvariable"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// assign keeps readers behind G16's gate until the successful finish is confirmed.
func (c *Coordinator) assign(ctx context.Context, request Request, inputs ir.Object, observation invocation.Observation,
	started time.Time, sequence int64, finish execution.FinishAttemptRequest, outcome outcomes.Result,
) (execution.CheckpointResult, execution.FinishAttemptRequest, outcomes.Result, error) {
	write, err := setvariable.Prepare(request.Node.Config, inputs)
	checkpointStarted := false
	var committed execution.CheckpointResult
	if err == nil {
		write.AttemptRef = request.Ref
		committed, err = request.VariableStore.Apply(ctx, write, func(ctx context.Context) (execution.CheckpointResult, error) {
			if err := entryError(ctx, request); err != nil {
				return execution.CheckpointResult{}, err
			}
			checkpointStarted = true
			result, err := c.dependencies.Checkpoints.FinishAttempt(ctx, finish)
			if err == nil && result.Disposition == constants.CheckpointCommitted {
				// Check this attempt's sequence before G16 authorizes publication.
				err = confirmed(result, sequence, "attempt_finish_unconfirmed")
			}
			return result, err
		})
	}
	if err == nil || checkpointStarted {
		return committed, finish, outcome, err
	}
	if errors.Is(err, xerror.ErrVariableInvalid) || errors.Is(err, xerror.ErrVariableUnavailable) {
		return execution.CheckpointResult{}, finish, outcome, err
	}
	var command xerror.CommandError
	if errors.As(err, &command) && command.Code == constants.CommandInputUnsupported {
		// A candidate rejection has not attempted persistence. Finalize it once.
	} else if contextError(ctx) != nil || snapshot(request).GroupCancellation {
		// Reuse owner/control authority, rather than inventing a command cancellation.
		err = nil
	} else {
		return execution.CheckpointResult{}, finish, outcome, err
	}
	finish, outcome, err = c.completion(ctx, request, observation, err, started)
	if err != nil {
		return execution.CheckpointResult{}, finish, outcome, err
	}
	committed, err = c.dependencies.Checkpoints.FinishAttempt(ctx, finish)
	return committed, finish, outcome, err
}
