// Package runner executes an admitted workflow sequentially for one device.
package runner

import (
	"context"
	"strings"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/attempts"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outputs"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/retry"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/routing"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/variables"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

type Dependencies struct {
	Bindings    *bindings.Resolver
	Invoker     *invocation.Invoker
	Outputs     *outputs.Validator
	Checkpoints attempts.Checkpoints
	Now         func() time.Time
}

type Request struct {
	RunGroupID        string
	DeviceExecutionID string
	ADBSerial         string
	WorkflowInputs    ir.Object
	Variables         bindings.VariableView
	VariableStore     *variables.Store
	Control           func() attempts.Control
}

// AttemptEvidence records acknowledgements, not a claim about current SQLite state.
type AttemptEvidence struct {
	Ref      execution.AttemptRef
	Began    bool
	Finished bool
	Sequence int64
	Cursor   string
}

// Result has a DeviceStatus only for a confirmed terminal device checkpoint.
// On an error, LastAttempt and NextNodeID are evidence for lifecycle settlement.
type Result struct {
	DeviceStatus  string
	TerminalError *xerror.CommandError
	NextNodeID    string
	LastAttempt   AttemptEvidence
}

type attemptRunner interface {
	Run(context.Context, attempts.Request) (attempts.Result, error)
}

type Runner struct {
	entryNodeID string
	nodes       map[string]ir.Node
	attempts    attemptRunner
}

// New borrows a G1-checked document immutably for this runner's lifetime.
func New(ctx context.Context, document *ir.Document, dependencies Dependencies) (*Runner, error) {
	const operation = "configure_device_runner"
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation(operation, err)
	}
	if document == nil || len(document.Nodes) == 0 || len(document.Nodes) > constants.RuntimeIRMaxNodes {
		return nil, runnerError(operation, "runner_document_invalid")
	}
	nodes := make(map[string]ir.Node, len(document.Nodes))
	for _, node := range document.Nodes {
		if err := ctx.Err(); err != nil {
			return nil, xerror.WithOperation(operation, err)
		}
		if _, exists := nodes[node.ID]; exists || !execution.ValidNodeID(node.ID) {
			return nil, runnerError(operation, "runner_document_invalid")
		}
		nodes[node.ID] = node
	}
	if _, exists := nodes[document.EntryNodeID]; !exists {
		return nil, runnerError(operation, "runner_document_invalid")
	}
	routes, err := routing.New(ctx, *document)
	if err != nil {
		return nil, xerror.WithOperation(operation, err)
	}
	coordinator, err := attempts.New(attempts.Dependencies{Bindings: dependencies.Bindings, Invoker: dependencies.Invoker,
		Outputs: dependencies.Outputs, Routes: routes, Checkpoints: dependencies.Checkpoints, Now: dependencies.Now})
	if err != nil {
		return nil, xerror.WithOperation(operation, err)
	}
	return &Runner{entryNodeID: document.EntryNodeID, nodes: nodes, attempts: coordinator}, nil
}

// Run owns one new device execution. Its context belongs to the execution owner,
// not the HTTP request that admitted work. A nil error may accompany durable failure.
func (r *Runner) Run(ctx context.Context, request Request) (Result, error) {
	const operation = "run_device_workflow"
	if r == nil || r.attempts == nil || len(r.nodes) == 0 {
		return Result{}, runnerError(operation, "runner_dependencies_unavailable")
	}
	if !execution.ValidID(request.RunGroupID) || !execution.ValidID(request.DeviceExecutionID) ||
		strings.TrimSpace(request.ADBSerial) == "" || request.WorkflowInputs == nil ||
		!request.Variables.ValidFor(request.RunGroupID, request.DeviceExecutionID) {
		return Result{}, runnerError(operation, "runner_request_invalid")
	}
	result := Result{NextNodeID: r.entryNodeID}
	inputs, err := copyInputs(ctx, request)
	if err != nil {
		return result, err
	}
	values := bindings.Values{WorkflowInputs: inputs, CommittedOutputs: make(map[string]ir.Object), Variables: request.Variables}
	visited := make(map[string]bool)
	number, previousID, expected := 1, "", constants.ExecutionQueued
	for {
		if err := lifecycleError(ctx, request); err != nil {
			return result, err
		}
		node, exists := r.nodes[result.NextNodeID]
		if !exists || (number == 1 && visited[node.ID]) {
			return result, runnerError(operation, "runner_transition_invalid")
		}
		visited[node.ID] = true
		id, err := uuid.NewRandom()
		if err != nil {
			return result, runnerError(operation, "runner_attempt_identity_unavailable")
		}
		if err := lifecycleError(ctx, request); err != nil {
			return result, err
		}
		ref := execution.AttemptRef{RunGroupID: request.RunGroupID, DeviceExecutionID: request.DeviceExecutionID, NodeID: node.ID, AttemptID: id.String()}
		completed, err := r.attempts.Run(ctx, attempts.Request{Ref: ref, Node: node, ExpectedDeviceStatus: expected,
			AttemptNumber: number, PreviousAttemptID: previousID, ADBSerial: request.ADBSerial, Values: values,
			VariableStore: request.VariableStore, Control: request.Control})
		result.LastAttempt = AttemptEvidence{Ref: ref, Began: completed.Began, Finished: completed.Finished, Sequence: completed.Sequence, Cursor: completed.Cursor}
		if err != nil {
			return result, xerror.WithOperation(operation, err)
		}
		if !completed.Began || !completed.Finished {
			return result, runnerError(operation, "runner_transition_invalid")
		}
		result.NextNodeID = completed.NextNodeID
		if completed.NextNodeID == "" {
			return terminalResult(result, node, completed)
		}
		if err := lifecycleError(ctx, request); err != nil {
			return result, err
		}
		if err := validContinuation(node, number, completed, r.nodes, visited); err != nil {
			return result, err
		}
		if completed.Outcome.Status == constants.ExecutionSucceeded {
			if err := publishOutput(node.ID, completed.Outcome.Output, values.CommittedOutputs); err != nil {
				return result, err
			}
		}
		if completed.Retry != nil {
			if err := lifecycleError(ctx, request); err != nil {
				return result, err
			}
			if err := retry.Wait(ctx, completed.Retry.DelayMS); err != nil {
				return result, xerror.WithOperation(operation, err)
			}
			if err := lifecycleError(ctx, request); err != nil {
				return result, err
			}
			number, previousID = number+1, ref.AttemptID
		} else {
			number, previousID = 1, ""
		}
		expected = constants.ExecutionRunning
	}
}
