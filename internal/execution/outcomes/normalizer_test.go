package outcomes_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outcomes"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outputs"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/delay"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func expect(t *testing.T, got, want outcomes.Result) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Normalize = %+v (error=%+v, output=%s), want %+v (error=%+v, output=%s)",
			got, got.Error, got.Output, want, want.Error, want.Output)
	}
}

func stopped(code string) outcomes.Result {
	message := constants.OutcomeInternalErrorMessage
	if code == constants.PersistenceFailed {
		message = constants.PersistenceFailedMessage
	}
	return outcomes.Result{Status: constants.ExecutionFailed, Stop: true,
		Error: &xerror.CommandError{Code: code, Message: message}}
}

func timeout(stop bool) outcomes.Result {
	return outcomes.Result{Status: constants.ExecutionTimedOut, Stop: stop,
		Error: &xerror.CommandError{Code: constants.NodeTimedOut, Message: constants.NodeTimedOutMessage}}
}

func catalog(t *testing.T) *nodecontract.Registry {
	t.Helper()
	definitions, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return definitions
}

func validator(t *testing.T) *outputs.Validator {
	t.Helper()
	v, err := outputs.New(catalog(t))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSupportedCommandMappings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		code, status    string
		stop, retryable bool
	}{
		{constants.CommandStartFailed, constants.ExecutionFailed, false, true},
		{constants.ADBUnavailable, constants.ExecutionFailed, false, true},
		{constants.CommandInputUnsupported, constants.ExecutionFailed, false, false},
		{constants.CommandFailed, constants.ExecutionFailed, false, false},
		{constants.NodeOutputInvalid, constants.ExecutionFailed, false, false},
		{constants.CommandTimedOut, constants.ExecutionTimedOut, false, false},
		{constants.NodeTimedOut, constants.ExecutionTimedOut, false, false},
		{constants.DeviceNotFound, constants.ExecutionFailed, true, false},
		{constants.DeviceUnauthorized, constants.ExecutionFailed, true, false},
		{constants.DeviceOffline, constants.ExecutionFailed, true, false},
		{constants.DeviceNotReady, constants.ExecutionFailed, true, false},
		{constants.DeviceDisconnected, constants.ExecutionFailed, true, false},
	} {
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()
			for _, form := range []struct {
				name string
				wrap func(xerror.CommandError) error
			}{
				{"value", func(e xerror.CommandError) error { return e }},
				{"pointer", func(e xerror.CommandError) error { return &e }},
				{"wrapped", func(e xerror.CommandError) error {
					return &xerror.OperationError{Operation: "protected operation", Detail: "protected detail",
						Err: fmt.Errorf("protected wrapper: %w", &e)}
				}},
			} {
				t.Run(form.name, func(t *testing.T) {
					command := xerror.CommandError{Code: tc.code, Message: "Approved producer explanation", Retryable: true}
					got := outcomes.Normalize(t.Context(), invocation.Observation{
						HandlerError: form.wrap(command), Output: map[string]any{"protected": "discard"},
					}, json.RawMessage(`{"protected":"discard"}`), nil)
					command.Retryable = tc.retryable
					expect(t, got, outcomes.Result{Status: tc.status, Error: &command, Stop: tc.stop})
				})
			}
		})
	}
	for _, code := range []string{constants.CommandStartFailed, constants.ADBUnavailable} {
		t.Run(code+" does not upgrade false", func(t *testing.T) {
			command := xerror.CommandError{Code: code, Message: "Approved producer explanation"}
			expect(t, outcomes.Normalize(t.Context(), invocation.Observation{}, nil, command),
				outcomes.Result{Status: constants.ExecutionFailed, Error: &command})
		})
	}
}

func TestFatalErrorsOverrideCancellation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, code string
		err        error
	}{
		{"persistence", constants.PersistenceFailed, xerror.ErrExecutionPersistence},
		{"unknown commit", constants.PersistenceFailed, xerror.ErrExecutionCommitUnknown},
		{"fatal checkpoint", constants.PersistenceFailed, xerror.ErrCheckpointFatal},
		{"stopped checkpoint", constants.PersistenceFailed, xerror.ErrCheckpointStopped},
		{"invocation invariant", constants.InternalError, xerror.ErrInvocationInvalid},
		{"routing invariant", constants.InternalError, xerror.ErrRoutingInvalid},
		{"binding dependencies", constants.InternalError, xerror.ErrInputBindingDependencies},
		{"output dependencies", constants.InternalError, xerror.ErrOutputValidationDependencies},
		{"invalid repository input", constants.InternalError, xerror.ErrExecutionInvalidInput},
		{"stale execution", constants.InternalError, xerror.ErrExecutionStaleState},
		{"recovery integrity", constants.InternalError, xerror.ErrRecoveryIntegrity},
		{"typed internal error", constants.InternalError, xerror.CommandError{Code: constants.InternalError, Message: "protected infrastructure text", Retryable: true}},
		{"typed persistence error", constants.PersistenceFailed, &xerror.CommandError{Code: constants.PersistenceFailed, Message: "protected storage text", Retryable: true}},
		{"unknown error", constants.InternalError, errors.New("protected unclassified error")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			err := fmt.Errorf("protected wrapper: %w", tc.err)
			expect(t, outcomes.Normalize(ctx, invocation.Observation{}, nil, err), stopped(tc.code))
			expect(t, outcomes.Normalize(ctx, invocation.Observation{HandlerError: err}, nil, nil), stopped(tc.code))
		})
	}
}

func TestSignalPrecedence(t *testing.T) {
	t.Parallel()
	startFailure := xerror.CommandError{Code: constants.CommandStartFailed, Message: "Process could not start", Retryable: true}
	cancelled := outcomes.Result{Status: constants.ExecutionCancelled, Stop: true}
	for _, tc := range []struct {
		name        string
		observation invocation.Observation
		stage       error
		want        outcomes.Result
	}{
		{"captured parent cancellation over handler failure", invocation.Observation{ParentError: context.Canceled, HandlerError: startFailure}, nil, cancelled},
		{"parent cancellation over child timeout", invocation.Observation{ParentError: context.Canceled, AttemptError: context.DeadlineExceeded}, nil, cancelled},
		{"parent deadline over child cancellation", invocation.Observation{ParentError: context.DeadlineExceeded, AttemptError: context.Canceled}, nil, timeout(true)},
		{"attempt deadline over retryable failure", invocation.Observation{AttemptError: context.DeadlineExceeded, HandlerError: startFailure}, nil, timeout(false)},
		{"attempt deadline", invocation.Observation{AttemptError: fmt.Errorf("wrapped: %w", context.DeadlineExceeded)}, nil, timeout(false)},
		{"handler deadline", invocation.Observation{HandlerError: xerror.WithOperation("dispatch_command", context.DeadlineExceeded)}, nil, timeout(false)},
		{"pre-call deadline", invocation.Observation{}, context.DeadlineExceeded, timeout(false)},
		{"isolated child cancellation", invocation.Observation{AttemptError: context.Canceled}, nil, stopped(constants.InternalError)},
		{"isolated command cancellation", invocation.Observation{HandlerError: context.Canceled}, nil, stopped(constants.InternalError)},
		{"isolated pre-call cancellation", invocation.Observation{}, context.Canceled, stopped(constants.InternalError)},
		{"persistence over retryable error and cancellation", invocation.Observation{ParentError: context.Canceled, HandlerError: startFailure}, xerror.ErrExecutionCommitUnknown, stopped(constants.PersistenceFailed)},
		{"persistence inside handler overrides stage", invocation.Observation{HandlerError: xerror.ErrExecutionPersistence}, startFailure, stopped(constants.PersistenceFailed)},
		{"two ordinary error stages", invocation.Observation{HandlerError: startFailure}, startFailure, stopped(constants.InternalError)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			expect(t, outcomes.Normalize(t.Context(), tc.observation, json.RawMessage(`{}`), tc.stage), tc.want)
		})
	}
	t.Run("current parent cancellation over checked output", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		expect(t, outcomes.Normalize(ctx, invocation.Observation{Output: map[string]any{}}, json.RawMessage(`{}`), nil), cancelled)
	})
	t.Run("elapsed parent before timer signal", func(t *testing.T) {
		ctx := fixedContext{Context: t.Context(), deadline: time.Unix(1, 0)}
		expect(t, outcomes.Normalize(ctx, invocation.Observation{}, nil, startFailure), timeout(true))
		ctx.err = context.Canceled
		expect(t, outcomes.Normalize(ctx, invocation.Observation{}, nil, startFailure), cancelled)
	})
}

func TestMalformedEvidenceStopsSafely(t *testing.T) {
	t.Parallel()
	var nilCommand *xerror.CommandError
	for _, tc := range []struct {
		name        string
		observation invocation.Observation
		stage       error
	}{
		{"nil typed error", invocation.Observation{}, nilCommand},
		{"wrapped nil typed error", invocation.Observation{}, xerror.WithOperation("protected", nilCommand)},
		{"unknown parent observation", invocation.Observation{ParentError: errors.New("protected parent error")}, nil},
		{"unknown attempt observation", invocation.Observation{AttemptError: errors.New("protected child error")}, nil},
		{"contradictory context signal", invocation.Observation{ParentError: errors.Join(context.Canceled, context.DeadlineExceeded)}, nil},
		{"joined retryable and unknown", invocation.Observation{}, errors.Join(xerror.CommandError{Code: constants.CommandStartFailed, Message: "Safe", Retryable: true}, errors.New("protected cause"))},
		{"wrapped joined contexts", invocation.Observation{}, fmt.Errorf("protected: %w", errors.Join(context.Canceled, context.DeadlineExceeded))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			expect(t, outcomes.Normalize(t.Context(), tc.observation, nil, tc.stage), stopped(constants.InternalError))
		})
	}
	t.Run("joined persistence retains highest priority", func(t *testing.T) {
		err := fmt.Errorf("protected wrapper: %w", errors.Join(context.Canceled, xerror.ErrExecutionPersistence))
		expect(t, outcomes.Normalize(t.Context(), invocation.Observation{}, nil, err), stopped(constants.PersistenceFailed))
	})
	t.Run("invalid current parent signal", func(t *testing.T) {
		ctx := fixedContext{Context: t.Context(), err: errors.New("protected current context")}
		expect(t, outcomes.Normalize(ctx, invocation.Observation{}, nil, nil), stopped(constants.InternalError))
	})
	for _, code := range []string{
		"", "UNKNOWN", "INVALID_REQUEST", "UNAUTHORIZED", "RESOURCE_NOT_FOUND", "PAYLOAD_TOO_LARGE",
		"RUNTIME_BUSY", "RUNTIME_INCOMPATIBLE", "VALIDATION_FAILED", "INVALID_EXECUTION_STATE", "EVENT_CURSOR_EXPIRED", "EXECUTION_INTERRUPTED",
	} {
		t.Run("unsupported code "+code, func(t *testing.T) {
			err := xerror.CommandError{Code: code, Message: "Safe", Retryable: true}
			expect(t, outcomes.Normalize(t.Context(), invocation.Observation{}, nil, err), stopped(constants.InternalError))
		})
	}
	for _, tc := range []struct{ name, message string }{
		{"empty message", ""},
		{"invalid UTF8", string([]byte{0xff})},
		{"too many ASCII characters", strings.Repeat("x", constants.MaxRuntimeErrorMessageChars+1)},
		{"too many Unicode characters", strings.Repeat("🌟", constants.MaxRuntimeErrorMessageChars+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := xerror.CommandError{Code: constants.CommandFailed, Message: tc.message}
			expect(t, outcomes.Normalize(t.Context(), invocation.Observation{}, nil, err), stopped(constants.InternalError))
		})
	}
	t.Run("Unicode character ceiling is accepted", func(t *testing.T) {
		err := xerror.CommandError{Code: constants.CommandFailed, Message: strings.Repeat("🌟", constants.MaxRuntimeErrorMessageChars)}
		expect(t, outcomes.Normalize(t.Context(), invocation.Observation{}, nil, err),
			outcomes.Result{Status: constants.ExecutionFailed, Error: &err})
	})
}

func TestCheckedOutputOwnershipAndMissingEvidence(t *testing.T) {
	t.Parallel()
	observation := invocation.Observation{Output: map[string]any{}}
	checked, err := validator(t).Validate(t.Context(), "flow.start", observation.Output)
	if err != nil {
		t.Fatal(err)
	}
	first := outcomes.Normalize(t.Context(), observation, checked, nil)
	second := outcomes.Normalize(t.Context(), observation, checked, nil)
	want := outcomes.Result{Status: constants.ExecutionSucceeded, Output: json.RawMessage(`{}`)}
	expect(t, first, want)
	expect(t, second, want)
	checked[0] = 'x'
	expect(t, first, want)
	first.Output[0] = 'y'
	expect(t, second, want)
	for _, tc := range []struct {
		name    string
		output  map[string]any
		checked json.RawMessage
	}{
		{"missing snapshot", map[string]any{}, nil},
		{"empty snapshot", map[string]any{}, json.RawMessage{}},
		{"nil handler output", nil, json.RawMessage(`{}`)},
		{"over ceiling", map[string]any{}, json.RawMessage(strings.Repeat("x", constants.MaxEventBytes+1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, outcomes.Normalize(t.Context(), invocation.Observation{Output: tc.output}, tc.checked, nil), stopped(constants.InternalError))
		})
	}
	producer := &xerror.CommandError{Code: constants.CommandFailed, Message: "Approved explanation"}
	a := outcomes.Normalize(t.Context(), invocation.Observation{}, nil, producer)
	b := outcomes.Normalize(t.Context(), invocation.Observation{}, nil, producer)
	producer.Message = "protected producer mutation"
	if a.Error.Message != "Approved explanation" {
		t.Fatal("result retained the producer's error pointer")
	}
	a.Error.Message = "caller mutation"
	if b.Error.Message != "Approved explanation" {
		t.Fatal("results share an error pointer")
	}
}

func TestExistingStageAndInvocationHandoffs(t *testing.T) {
	definitions := catalog(t)
	resolver, err := bindings.New(definitions)
	if err != nil {
		t.Fatal(err)
	}
	v := validator(t)
	for _, tc := range []struct {
		name string
		run  func() error
		code string
		stop bool
	}{
		{"missing prior output", func() error {
			_, err := resolver.Resolve(t.Context(), ir.Node{Type: "android.input_text", Config: ir.Object{}, Inputs: ir.Object{
				"text": json.RawMessage(`{"source":"NODE_OUTPUT","nodeId":"prior","field":"artifactId"}`),
			}}, "text", bindings.Values{})
			return err
		}, constants.CommandInputUnsupported, false},
		{"rejected invocation", func() error {
			var i *invocation.Invoker
			_, err := i.Invoke(t.Context(), ir.Node{}, registry.Invocation{})
			return err
		}, constants.InternalError, true},
		{"invalid handler output", func() error {
			_, err := v.Validate(t.Context(), "flow.delay", map[string]any{})
			return err
		}, constants.NodeOutputInvalid, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("fixture did not produce the expected real component error")
			}
			got := outcomes.Normalize(t.Context(), invocation.Observation{}, nil, err)
			if got.Status != constants.ExecutionFailed || got.Error == nil || got.Error.Code != tc.code || got.Error.Retryable || got.Stop != tc.stop {
				t.Fatalf("incorrect stage handoff: %+v, error=%+v", got, got.Error)
			}
		})
	}
	handlers, err := registry.New(definitions, []registry.Registration{{Type: "flow.delay", RuntimeHandler: "flow.delay", Run: delay.Invoke}})
	if err != nil {
		t.Fatal(err)
	}
	i, err := invocation.New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		timeoutMS int64
		elapsed   time.Duration
	}{
		{"real delay times out", 2000, 2 * time.Second},
		{"real delay succeeds", 6000, 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				observation, err := i.Invoke(t.Context(), ir.Node{Type: "flow.delay", RuntimeHandler: "flow.delay", TimeoutMS: tc.timeoutMS},
					registry.Invocation{Inputs: map[string]json.RawMessage{"durationMs": json.RawMessage("5000")}})
				if err != nil || time.Since(start) != tc.elapsed {
					t.Fatalf("G6 fixture failed: %v, elapsed=%v", err, time.Since(start))
				}
				if tc.timeoutMS == 2000 {
					expect(t, outcomes.Normalize(t.Context(), observation, nil, nil), timeout(false))
					return
				}
				checked, err := v.Validate(t.Context(), "flow.delay", observation.Output)
				if err != nil {
					t.Fatal(err)
				}
				expect(t, outcomes.Normalize(t.Context(), observation, checked, nil),
					outcomes.Result{Status: constants.ExecutionSucceeded, Output: json.RawMessage(`{"durationMs":5000}`)})
			})
		})
	}
}

func TestNormalizedResultsFitExistingEvents(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name   string
		result outcomes.Result
	}{
		{"success", outcomes.Normalize(t.Context(), invocation.Observation{Output: map[string]any{}}, json.RawMessage(`{}`), nil)},
		{"failure", outcomes.Normalize(t.Context(), invocation.Observation{}, nil, xerror.CommandError{Code: constants.CommandFailed, Message: "Approved failure"})},
		{"timeout", outcomes.Normalize(t.Context(), invocation.Observation{AttemptError: context.DeadlineExceeded}, nil, nil)},
		{"cancellation", outcomes.Normalize(ctx, invocation.Observation{}, nil, nil)},
		{"persistence stop", outcomes.Normalize(t.Context(), invocation.Observation{}, nil, xerror.ErrExecutionCommitUnknown)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(struct {
				Status        string               `json:"status"`
				AttemptNumber int                  `json:"attemptNumber"`
				Output        json.RawMessage      `json:"output,omitempty"`
				Error         *xerror.CommandError `json:"error,omitempty"`
			}{Status: tc.result.Status, AttemptNumber: 1, Output: tc.result.Output, Error: tc.result.Error})
			if err != nil {
				t.Fatal(err)
			}
			event := execution.PendingEvent{Type: constants.EventAttemptStateChanged, OccurredAt: time.Unix(1, 0),
				RunGroupID: "10000000-0000-4000-8000-000000000001", DeviceExecutionID: "10000000-0000-4000-8000-000000000002",
				NodeAttemptID: "10000000-0000-4000-8000-000000000003", NodeID: "node", Payload: payload}
			if !execution.ValidEvent(event) {
				t.Fatalf("normalized projection violates A6: %s", payload)
			}
		})
	}
}

func TestConcurrentNormalizationIsIsolated(t *testing.T) {
	t.Parallel()
	producer := &xerror.CommandError{Code: constants.CommandFailed, Message: "Approved explanation"}
	checked := json.RawMessage(`{}`)
	for n := range 16 {
		t.Run(fmt.Sprintf("execution %d", n), func(t *testing.T) {
			t.Parallel()
			result := outcomes.Normalize(t.Context(), invocation.Observation{}, nil, producer)
			result.Error.Message = "caller mutation"
			next := outcomes.Normalize(t.Context(), invocation.Observation{}, nil, producer)
			if next.Error.Message != "Approved explanation" {
				t.Fatal("parallel callers share error values")
			}
			result = outcomes.Normalize(t.Context(), invocation.Observation{Output: map[string]any{}}, checked, nil)
			result.Output[0] = 'x'
			if string(checked) != `{}` {
				t.Fatal("parallel callers share snapshot bytes")
			}
		})
	}
}

type fixedContext struct {
	context.Context
	err      error
	deadline time.Time
}

func (c fixedContext) Err() error { return c.err }

func (c fixedContext) Deadline() (time.Time, bool) { return c.deadline, !c.deadline.IsZero() }
