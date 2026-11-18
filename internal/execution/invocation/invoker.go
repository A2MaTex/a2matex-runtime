// Package invocation calls one registered handler within an attempt deadline.
package invocation

import (
	"context"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Observation is internal evidence for G7, not a persisted status or API payload.
// Output is retained only when the handler and both observed contexts succeeded.
type Observation struct {
	Output       map[string]any
	HandlerError error
	AttemptError error
	ParentError  error
}

type Invoker struct {
	handlers *registry.Registry
}

func New(handlers *registry.Registry) (*Invoker, error) {
	if handlers == nil {
		return nil, &xerror.OperationError{
			Operation: "configure_node_invocation", Detail: "invocation_registry_unavailable", Err: xerror.ErrInvocationInvalid,
		}
	}
	return &Invoker{handlers: handlers}, nil
}

// Invoke calls once and waits for the handler to return, including after cancellation.
// A nil outer error means a call returned; inspect the observation before accepting output.
// The caller supplies admitted config and resolved inputs and keeps them immutable during the call.
func (i *Invoker) Invoke(ctx context.Context, node ir.Node, call registry.Invocation) (Observation, error) {
	const operation = "invoke_node_handler"
	fail := func(reason string) (Observation, error) {
		if err := contextError(ctx); err != nil {
			return Observation{}, xerror.WithOperation(operation, err)
		}
		return Observation{}, &xerror.OperationError{
			Operation: operation,
			Detail:    reason,
			Err:       xerror.ErrInvocationInvalid,
		}
	}
	if err := contextError(ctx); err != nil {
		return Observation{}, xerror.WithOperation(operation, err)
	}
	if i == nil || i.handlers == nil {
		return fail("invocation_registry_unavailable")
	}
	if node.TimeoutMS < 1 || node.TimeoutMS > validation.NodeTimeoutMaxMS {
		return fail("invocation_timeout_invalid")
	}
	run, exists := i.handlers.Lookup(node.Type, node.RuntimeHandler)
	if !exists {
		return fail("invocation_handler_unavailable")
	}

	attemptCtx, cancel := context.WithTimeout(ctx, time.Duration(node.TimeoutMS)*time.Millisecond)
	defer cancel()
	if err := contextError(attemptCtx); err != nil {
		return Observation{}, xerror.WithOperation(operation, err)
	}
	output, handlerError := run(attemptCtx, call)
	observation := Observation{
		HandlerError: xerror.WithOperation(operation, handlerError),
		ParentError:  contextError(ctx),
		AttemptError: contextError(attemptCtx),
	}
	if observation.HandlerError == nil && observation.AttemptError == nil && observation.ParentError == nil {
		observation.Output = output
	}
	return observation, nil
}

func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// A deadline can be reached before its timer's cancellation is observed.
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}
