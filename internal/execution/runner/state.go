package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/attempts"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func copyInputs(ctx context.Context, request Request) (ir.Object, error) {
	if err := lifecycleError(ctx, request); err != nil {
		return nil, err
	}
	owned := make(ir.Object, len(request.WorkflowInputs))
	for name, value := range request.WorkflowInputs {
		if err := lifecycleError(ctx, request); err != nil {
			return nil, err
		}
		owned[name] = bytes.Clone(value)
	}
	return owned, nil
}

func lifecycleError(ctx context.Context, request Request) error {
	accepted := request.Control != nil && request.Control().GroupCancellation
	err := ctx.Err()
	if deadline, ok := ctx.Deadline(); err == nil && ok && !time.Now().Before(deadline) {
		err = context.DeadlineExceeded
	}
	if err == nil && accepted {
		err = context.Canceled
	}
	return xerror.WithOperation("run_device_workflow", err)
}

func publishOutput(nodeID string, output json.RawMessage, view map[string]ir.Object) error {
	if _, exists := view[nodeID]; exists {
		return runnerError("run_device_workflow", "runner_duplicate_output")
	}
	var snapshot ir.Object
	if json.Unmarshal(output, &snapshot) != nil || snapshot == nil {
		return runnerError("run_device_workflow", "runner_output_snapshot_invalid")
	}
	view[nodeID] = snapshot
	return nil
}

func validContinuation(node ir.Node, number int, result attempts.Result, nodes map[string]ir.Node, visited map[string]bool) error {
	fail := func() error { return runnerError("run_device_workflow", "runner_transition_invalid") }
	if _, exists := nodes[result.NextNodeID]; !exists || result.Outcome.Stop {
		return fail()
	}
	if decision := result.Retry; decision != nil {
		if result.NextNodeID != node.ID || result.Outcome.Status != constants.ExecutionFailed || result.Outcome.Error == nil ||
			!result.Outcome.Error.Retryable || int64(number) >= node.RetryPolicy.MaxAttempts ||
			int64(decision.MaxAttempts) != node.RetryPolicy.MaxAttempts || int64(decision.DelayMS) != node.RetryPolicy.DelayMS {
			return fail()
		}
		return nil
	}
	if visited[result.NextNodeID] {
		return fail()
	}
	switch result.Outcome.Status {
	case constants.ExecutionSucceeded, constants.ExecutionFailed, constants.ExecutionTimedOut:
		return nil
	default:
		return fail()
	}
}

func terminalResult(result Result, node ir.Node, completed attempts.Result) (Result, error) {
	valid := completed.Retry == nil
	switch completed.Outcome.Status {
	case constants.ExecutionSucceeded:
		valid = valid && node.Type == constants.EndNodeType && !completed.Outcome.Stop && completed.Outcome.Error == nil
		result.DeviceStatus = constants.ExecutionSucceeded
	case constants.ExecutionFailed, constants.ExecutionTimedOut:
		valid = valid && completed.Outcome.Error != nil
		result.DeviceStatus = constants.ExecutionFailed
		if completed.Outcome.Error != nil {
			safe := *completed.Outcome.Error
			result.TerminalError = &safe
		}
	case constants.ExecutionCancelled:
		valid = valid && completed.Outcome.Stop && completed.Outcome.Error == nil
		result.DeviceStatus = constants.ExecutionCancelled
	default:
		valid = false
	}
	if !valid {
		result.DeviceStatus, result.TerminalError = "", nil
		return result, runnerError("run_device_workflow", "runner_transition_invalid")
	}
	return result, nil
}

func runnerError(operation, detail string) error {
	return &xerror.OperationError{Operation: operation, Detail: detail, Err: xerror.ErrRunnerInvalid}
}
