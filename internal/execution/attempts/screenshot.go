package attempts

import (
	"context"
	"encoding/json"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/flow"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type ScreenshotArtifacts interface {
	Stage(context.Context, string, []byte) (execution.Artifact, error)
	DiscardUncommitted(context.Context, string)
}

// screenshotStage belongs to one synchronous invocation, never the shared runner.
type screenshotStage struct {
	store                     ScreenshotArtifacts
	attemptID                 string
	artifact                  *execution.Artifact
	called, closed, submitted bool
	failure                   error
}

func (s *screenshotStage) stage(owner, ctx context.Context, data []byte) (id string, err error) {
	defer func() {
		if err != nil && s.failure == nil {
			s.failure = err
		}
	}()
	if s.closed || s.called {
		return "", screenshotGuard("screenshot_stage_reused")
	}
	s.called = true
	if err := contextError(owner); err != nil {
		return "", xerror.WithOperation("stage_screenshot_attempt", err)
	}
	if err := flow.CheckAttempt(ctx); err != nil {
		return "", xerror.WithOperation("stage_screenshot_attempt", err)
	}
	artifact, err := s.store.Stage(ctx, s.attemptID, data)
	if err != nil {
		return "", xerror.WithOperation("stage_screenshot_attempt", err)
	}
	if !artifact.Valid() || artifact.AttemptID != s.attemptID {
		// Unknown ownership cannot authorize deletion of the returned ID.
		return "", screenshotGuard("screenshot_stage_invalid")
	}
	// Retain the owned record before cancellation can hide the handler's output.
	s.artifact = &artifact
	if err := contextError(owner); err != nil {
		return "", xerror.WithOperation("stage_screenshot_attempt", err)
	}
	if err := contextError(ctx); err != nil {
		return "", xerror.WithOperation("stage_screenshot_attempt", err)
	}
	return artifact.ID, nil
}

func (s *screenshotStage) validate(output json.RawMessage) error {
	if s.artifact == nil {
		return screenshotGuard("screenshot_stage_missing")
	}
	var fields map[string]string
	if json.Unmarshal(output, &fields) != nil || len(fields) != 1 || fields["artifactId"] != s.artifact.ID {
		return xerror.WithOperation("pair_screenshot_output", xerror.CommandError{
			Code: constants.NodeOutputInvalid, Message: "Screenshot output does not match its staged artifact",
		})
	}
	return nil
}

func (s *screenshotStage) attach(finish *execution.FinishAttemptRequest) {
	if s.artifact == nil {
		return
	}
	if finish.Status == constants.ExecutionSucceeded {
		finish.Artifact = s.artifact
	} else {
		finish.DiscardArtifactID = s.artifact.ID
	}
}

func (s *screenshotStage) cleanup(owner context.Context, request Request) {
	s.closed = true
	if s.submitted || s.artifact == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(owner), constants.CheckpointTimeout)
	defer cancel()
	if deadline := snapshot(request).ShutdownDeadline; !deadline.IsZero() {
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, deadline)
		defer stop()
	}
	s.store.DiscardUncommitted(ctx, s.artifact.ID)
}

func screenshotGuard(reason string) error {
	return &xerror.OperationError{Operation: "stage_screenshot_attempt", Detail: reason,
		Err: xerror.CommandError{Code: constants.InternalError, Message: "Screenshot staging contract is invalid"}}
}
