// Package variables owns bounded, run-local RAM state without persistence.
package variables

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type scopeState struct {
	values map[string]json.RawMessage
	bytes  int
}

// Store is bound to one newly admitted RunGroup. It starts empty and is not restored.
// Its checkpoint closure must not reenter the store; G18 owns lifecycle integration.
type Store struct {
	groupID     string
	gate        chan struct{}
	flows       map[string]*scopeState
	global      scopeState
	unavailable bool
}

func New(runGroupID string, deviceExecutionIDs []string) (*Store, error) {
	if !execution.ValidID(runGroupID) || len(deviceExecutionIDs) < 1 {
		return nil, xerror.WithOperation("configure_scoped_variables", xerror.ErrVariableInvalid)
	}
	s := &Store{groupID: runGroupID, gate: make(chan struct{}, 1), flows: make(map[string]*scopeState, len(deviceExecutionIDs))}
	for _, id := range deviceExecutionIDs {
		if !execution.ValidID(id) || s.flows[id] != nil {
			return nil, xerror.WithOperation("configure_scoped_variables", xerror.ErrVariableInvalid)
		}
		s.flows[id] = &scopeState{}
	}
	return s, nil
}

// Get returns an independent scalar copy. Missing is distinct from false, zero, or "".
func (s *Store) Get(ctx context.Context, deviceID string, scope constants.VariableScope, name string) (json.RawMessage, bool, error) {
	const operation = "read_scoped_variable"
	if err := s.lock(ctx); err != nil {
		return nil, false, xerror.WithOperation(operation, err)
	}
	defer s.unlock()
	target, err := s.resolve(deviceID, scope, name)
	if err != nil {
		return nil, false, xerror.WithOperation(operation, err)
	}
	value, found := target.values[name]
	return bytes.Clone(value), found, nil
}

// Apply publishes only after its trusted closure confirms this attempt's checkpoint.
// The closure captures the exact successful FinishAttempt request, not variable data.
func (s *Store) Apply(ctx context.Context, write execution.VariableWrite,
	checkpoint func(context.Context) (execution.CheckpointResult, error),
) (execution.CheckpointResult, error) {
	const operation = "apply_scoped_variable"
	fail := func(err error) (execution.CheckpointResult, error) {
		return execution.CheckpointResult{}, xerror.WithOperation(operation, err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if s == nil || s.gate == nil || checkpoint == nil || write.RunGroupID != s.groupID ||
		!execution.ValidID(write.AttemptID) || !execution.ValidNodeID(write.NodeID) {
		return fail(xerror.ErrVariableInvalid)
	}
	value, err := execution.EncodeVariableScalar(write.Value)
	if err != nil {
		return fail(err)
	}
	if err := s.lock(ctx); err != nil {
		return fail(err)
	}
	defer s.unlock()
	target, err := s.resolve(write.DeviceExecutionID, write.Scope, write.Name)
	if err != nil {
		return fail(err)
	}
	old, present := target.values[write.Name]
	size := target.bytes + len(value) - len(old)
	if !present {
		size += len(write.Name)
	}
	if (!present && len(target.values) >= constants.MaxVariablesPerScope) || size > constants.MaxVariableScopeBytes {
		return fail(xerror.CommandError{Code: constants.CommandInputUnsupported, Message: "Variable scope exceeds its name or byte limit"})
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	result, err := checkpoint(ctx)
	if err != nil {
		s.unavailable = true
		return fail(err)
	}
	if result.Disposition == constants.CheckpointAlreadyFinalized || result.Disposition == constants.CheckpointStale {
		return result, nil
	}
	cursor, cursorErr := execution.ParseEventCursor(result.Cursor)
	if result.Disposition != constants.CheckpointCommitted || result.Sequence < 1 || cursorErr != nil || cursor < 1 {
		s.unavailable = true
		return fail(xerror.ErrVariableInvalid)
	}
	// A confirmed checkpoint wins a cancellation that arrived during settlement.
	if target.values == nil {
		target.values = make(map[string]json.RawMessage)
	}
	target.values[write.Name], target.bytes = value, size
	return result, nil
}

// Close releases maps after the owner has joined workers; it is idempotent.
func (s *Store) Close(ctx context.Context) error {
	if err := s.lock(ctx); err != nil {
		return xerror.WithOperation("close_scoped_variables", err)
	}
	defer s.unlock()
	s.flows, s.global, s.unavailable = nil, scopeState{}, true
	return nil
}

func (s *Store) resolve(deviceID string, scope constants.VariableScope, name string) (*scopeState, error) {
	if s.unavailable {
		return nil, xerror.ErrVariableUnavailable
	}
	flow, registered := s.flows[deviceID]
	if !registered || !ir.ValidVariableName(name) {
		return nil, xerror.ErrVariableInvalid
	}
	switch scope {
	case constants.VariableScopeFlow:
		return flow, nil
	case constants.VariableScopeRunGroup:
		return &s.global, nil
	}
	return nil, xerror.ErrVariableInvalid
}

func (s *Store) lock(ctx context.Context) error {
	if s == nil || s.gate == nil {
		return xerror.ErrVariableInvalid
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			s.unlock()
			return err
		}
		return nil
	}
}

func (s *Store) unlock() { <-s.gate }
