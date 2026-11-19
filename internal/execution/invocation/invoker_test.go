package invocation_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/delay"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/switcher"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func invoker(t *testing.T, nodeType string, run registry.Handler) *invocation.Invoker {
	t.Helper()
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := registry.New(catalog, []registry.Registration{{Type: nodeType, RuntimeHandler: nodeType, Run: run}})
	if err != nil {
		t.Fatal(err)
	}
	i, err := invocation.New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func node(nodeType string, timeoutMS int64) ir.Node {
	return ir.Node{ID: "node", Type: nodeType, RuntimeHandler: nodeType, TimeoutMS: timeoutMS}
}

func checkGuardError(t *testing.T, got invocation.Observation, err error, operation, reason string) {
	t.Helper()
	if got.Output != nil || got.HandlerError != nil || got.AttemptError != nil || got.ParentError != nil {
		t.Fatalf("guard failure returned an observation: %+v", got)
	}
	var failure *xerror.OperationError
	var command xerror.CommandError
	if !errors.Is(err, xerror.ErrInvocationInvalid) || !errors.As(err, &failure) ||
		failure.Operation != operation || failure.Detail != reason || errors.As(err, &command) {
		t.Fatalf("unexpected invocation failure: %v (%+v)", err, failure)
	}
	if err.Error() != operation+": node invocation is unavailable or invalid" {
		t.Fatalf("unsafe invocation error: %v", err)
	}
}

func TestSwitchLazyResolutionUsesAttemptDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		i := invoker(t, constants.SwitchNodeType, switcher.Invoke)
		calls := 0
		got, err := i.Invoke(t.Context(), node(constants.SwitchNodeType, 1), registry.Invocation{
			Config: map[string]json.RawMessage{"rules": []byte(`[{"id":"bfbb31bc-d4b5-44ec-b9eb-a09d68e74083","operator":"IS_TRUE"}]`)},
			ResolveInput: func(ctx context.Context, field string) (json.RawMessage, error) {
				calls++
				if _, exists := ctx.Deadline(); !exists || field != "value" {
					t.Fatal("resolver did not receive the attempt context")
				}
				<-ctx.Done()
				return []byte("true"), nil
			},
		})
		if err != nil || calls != 1 || got.Output != nil || got.SelectedPort != "" || !errors.Is(got.AttemptError, context.DeadlineExceeded) || !errors.Is(got.HandlerError, context.DeadlineExceeded) {
			t.Fatalf("late resolver success: %+v %v calls=%d", got, err, calls)
		}
	})
}

func TestSwitchSelectionObservation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		selections []string
		failure    error
		wantPort   string
		guard      bool
	}{
		{"selected", []string{"default"}, nil, "default", false},
		{"missing", nil, nil, "", true},
		{"empty", []string{""}, nil, "", true},
		{"duplicate", []string{"default", "default"}, nil, "", true},
		{"failed after selection", []string{"default"}, xerror.CommandError{Code: constants.CommandInputUnsupported, Message: "Switch input invalid"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := invoker(t, constants.SwitchNodeType, func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
				for _, port := range tc.selections {
					_ = call.SelectPort(ctx, port)
				} // Deliberately ignore guards to test invoker enforcement.
				return map[string]any{}, tc.failure
			})
			got, err := i.Invoke(t.Context(), node(constants.SwitchNodeType, 1000), registry.Invocation{})
			if err != nil || got.SelectedPort != tc.wantPort || errors.Is(got.HandlerError, xerror.ErrInvocationInvalid) != tc.guard {
				t.Fatalf("%+v %v", got, err)
			}
			if (tc.guard || tc.failure != nil) && got.Output != nil {
				t.Fatal("failed call leaked output")
			}
		})
	}
	for _, parentStop := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			i := invoker(t, constants.SwitchNodeType, func(attempt context.Context, call registry.Invocation) (map[string]any, error) {
				if err := call.SelectPort(attempt, "default"); err != nil {
					t.Fatal(err)
				}
				if parentStop {
					cancel()
				} else {
					time.Sleep(time.Second)
				}
				return map[string]any{}, nil
			})
			got, err := i.Invoke(ctx, node(constants.SwitchNodeType, 1), registry.Invocation{})
			if err != nil || got.Output != nil || got.SelectedPort != "" || got.HandlerError != nil {
				t.Fatalf("late success: %+v %v", got, err)
			}
			if parentStop && !errors.Is(got.ParentError, context.Canceled) {
				t.Fatal(got.ParentError)
			}
			if !parentStop && !errors.Is(got.AttemptError, context.DeadlineExceeded) {
				t.Fatal(got.AttemptError)
			}
		})
	}
	i := invoker(t, "flow.start", func(_ context.Context, call registry.Invocation) (map[string]any, error) {
		if call.SelectPort != nil || call.ResolveInput != nil {
			t.Fatal("ordinary handler received control capabilities")
		}
		return map[string]any{}, nil
	})
	if _, err := i.Invoke(t.Context(), node("flow.start", 1000), registry.Invocation{
		SelectPort: func(context.Context, string) error { t.Fatal("caller selected a route"); return nil },
		ResolveInput: func(context.Context, string) (json.RawMessage, error) {
			t.Fatal("caller resolved an input")
			return nil, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNewAndInvocationGuards(t *testing.T) {
	t.Parallel()
	i, err := invocation.New(nil)
	if i != nil {
		t.Fatal("missing registry returned an invoker")
	}
	checkGuardError(t, invocation.Observation{}, err, "configure_node_invocation", "invocation_registry_unavailable")
	var calls atomic.Int32
	i = invoker(t, "android.tap", func(context.Context, registry.Invocation) (map[string]any, error) {
		calls.Add(1)
		return map[string]any{}, nil
	})
	for _, tc := range []struct {
		name, reason string
		change       func(*ir.Node)
	}{
		{"zero timeout", "invocation_timeout_invalid", func(n *ir.Node) { n.TimeoutMS = 0 }},
		{"negative timeout", "invocation_timeout_invalid", func(n *ir.Node) { n.TimeoutMS = -1 }},
		{"over limit", "invocation_timeout_invalid", func(n *ir.Node) { n.TimeoutMS = validation.NodeTimeoutMaxMS + 1 }},
		{"overflow timeout", "invocation_timeout_invalid", func(n *ir.Node) { n.TimeoutMS = math.MaxInt64 }},
		{"unknown type", "invocation_handler_unavailable", func(n *ir.Node) { n.Type = "protected type" }},
		{"wrong key", "invocation_handler_unavailable", func(n *ir.Node) { n.RuntimeHandler = "protected key" }},
		{"wrong pair", "invocation_handler_unavailable", func(n *ir.Node) { n.Type = "android.swipe" }},
		{"case sensitive", "invocation_handler_unavailable", func(n *ir.Node) { n.Type = "Android.tap" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := node("android.tap", 1000)
			tc.change(&n)
			got, err := i.Invoke(t.Context(), n, registry.Invocation{})
			checkGuardError(t, got, err, "invoke_node_handler", tc.reason)
		})
	}
	for _, tc := range []struct {
		name    string
		invoker *invocation.Invoker
	}{
		{name: "nil invoker"},
		{name: "zero invoker", invoker: &invocation.Invoker{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.invoker.Invoke(t.Context(), node("android.tap", 1000), registry.Invocation{})
			checkGuardError(t, got, err, "invoke_node_handler", "invocation_registry_unavailable")
		})
	}
	if calls.Load() != 0 {
		t.Fatal("invalid invocation reached a handler")
	}
}

func TestInvocationForwardsCallAndCleansUp(t *testing.T) {
	t.Parallel()
	type contextKey struct{}
	parent := context.WithValue(t.Context(), contextKey{}, "execution")
	var attempt context.Context
	calls := 0
	i := invoker(t, "android.screenshot", func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
		calls++
		attempt = ctx
		if _, ok := ctx.Deadline(); !ok || ctx.Value(contextKey{}) != "execution" ||
			call.ADBSerial != "emulator-5554" || string(call.Config["targetMode"]) != `"FULL_SCREEN"` ||
			string(call.Inputs["example"]) != `"resolved input"` || call.StageScreenshot == nil {
			t.Fatal("handler did not receive the bounded context and exact call")
		}
		id, err := call.StageScreenshot(ctx, []byte("controlled PNG"))
		return map[string]any{"artifactId": id}, err
	})
	call := registry.Invocation{
		ADBSerial: "emulator-5554",
		Config:    map[string]json.RawMessage{"targetMode": json.RawMessage(`"FULL_SCREEN"`)},
		Inputs:    map[string]json.RawMessage{"example": json.RawMessage(`"resolved input"`)},
		StageScreenshot: func(ctx context.Context, data []byte) (string, error) {
			if ctx != attempt || string(data) != "controlled PNG" {
				t.Fatal("staging callback changed")
			}
			return "stage-reference", nil
		},
	}
	got, err := i.Invoke(parent, node("android.screenshot", 1000), call)
	if err != nil || got.HandlerError != nil || got.AttemptError != nil || got.ParentError != nil ||
		got.Output["artifactId"] != "stage-reference" || calls != 1 {
		t.Fatalf("unexpected invocation observation: %+v, %v, calls=%d", got, err, calls)
	}
	if !errors.Is(attempt.Err(), context.Canceled) || parent.Err() != nil {
		t.Fatal("attempt cleanup was missing or cancelled the parent")
	}
}

func TestHandlerResultsRemainUnnormalized(t *testing.T) {
	t.Parallel()
	raw := errors.New("controlled handler failure")
	command := xerror.CommandError{Code: constants.CommandStartFailed, Message: "controlled start failure", Retryable: true}
	for _, tc := range []struct {
		name   string
		output map[string]any
		err    error
	}{
		{name: "empty successful output", output: map[string]any{}},
		{name: "nil output awaits G3"},
		{name: "raw error", output: map[string]any{"protected": "discard"}, err: raw},
		{name: "contextual error", err: xerror.WithOperation("read_display", raw)},
		{name: "typed retryable error", err: command},
		{name: "inner deadline without expired attempt", err: context.DeadlineExceeded},
		{name: "inner cancellation without cancelled parent", err: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := invoker(t, "android.tap", func(context.Context, registry.Invocation) (map[string]any, error) {
				return tc.output, tc.err
			})
			got, err := i.Invoke(t.Context(), node("android.tap", 1000), registry.Invocation{})
			if err != nil || got.AttemptError != nil || got.ParentError != nil || !errors.Is(got.HandlerError, tc.err) {
				t.Fatalf("handler evidence changed: %+v, %v", got, err)
			}
			if tc.err != nil {
				if got.Output != nil {
					t.Fatal("failed handler output was retained")
				}
				var failure *xerror.OperationError
				if !errors.As(got.HandlerError, &failure) {
					t.Fatal("handler error lost its operation")
				}
				if tc.name == "contextual error" && failure.Operation != "read_display" {
					t.Fatal("more specific operation was replaced")
				}
			} else if (got.Output == nil) != (tc.output == nil) {
				t.Fatal("nil and empty output were normalized")
			}
		})
	}
}

func TestRealDelayAttemptBudget(t *testing.T) {
	i := invoker(t, "flow.delay", delay.Invoke)
	for _, tc := range []struct {
		name       string
		durationMS string
		timeoutMS  int64
		elapsed    time.Duration
		timedOut   bool
	}{
		{"five second wait completes", "5000", 6000, 5 * time.Second, false},
		{"five second wait times out", "5000", 2000, 2 * time.Second, true},
		{"equal timer and deadline", "2000", 2000, 2 * time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				got, err := i.Invoke(t.Context(), node("flow.delay", tc.timeoutMS), registry.Invocation{
					Inputs: map[string]json.RawMessage{"durationMs": json.RawMessage(tc.durationMS)},
				})
				if err != nil || got.ParentError != nil || time.Since(start) != tc.elapsed {
					t.Fatalf("delay observation: %+v, %v, elapsed=%v", got, err, time.Since(start))
				}
				if tc.timedOut {
					if got.Output != nil || !errors.Is(got.AttemptError, context.DeadlineExceeded) ||
						!errors.Is(got.HandlerError, context.DeadlineExceeded) {
						t.Fatalf("missing timeout evidence: %+v", got)
					}
				} else if got.HandlerError != nil || got.AttemptError != nil || got.Output["durationMs"] != 5000 {
					t.Fatalf("unexpected completed delay: %+v", got)
				}
			})
		})
	}
}

func TestLateHandlerReturnsAreJoinedAndDiscarded(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "late success"},
		{name: "late failure", err: errors.New("controlled late failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := invoker(t, "android.tap", func(context.Context, registry.Invocation) (map[string]any, error) {
				time.Sleep(3 * time.Second) // Deliberately ignores the two-second attempt budget.
				return map[string]any{"protected": "discard"}, tc.err
			})
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				got, err := i.Invoke(t.Context(), node("android.tap", 2000), registry.Invocation{})
				if err != nil || time.Since(start) != 3*time.Second || got.Output != nil || got.ParentError != nil ||
					!errors.Is(got.AttemptError, context.DeadlineExceeded) || !errors.Is(got.HandlerError, tc.err) {
					t.Fatalf("late return was detached or accepted: %+v, %v, elapsed=%v", got, err, time.Since(start))
				}
			})
		})
	}
}

func TestParentDeadlineAndCancellation(t *testing.T) {
	i := invoker(t, "flow.delay", delay.Invoke)
	t.Run("earlier parent deadline", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			parent, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			start := time.Now()
			got, err := i.Invoke(parent, node("flow.delay", 2000), registry.Invocation{
				Inputs: map[string]json.RawMessage{"durationMs": json.RawMessage("5000")},
			})
			if err != nil || got.Output != nil || time.Since(start) != time.Second ||
				!errors.Is(got.ParentError, context.DeadlineExceeded) || !errors.Is(got.AttemptError, context.DeadlineExceeded) {
				t.Fatalf("earlier parent deadline lost: %+v, %v", got, err)
			}
		})
	})
	t.Run("parent cancelled while running", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			var got invocation.Observation
			var err error
			done := make(chan struct{})
			go func() {
				defer close(done)
				got, err = i.Invoke(parent, node("flow.delay", 6000), registry.Invocation{
					Inputs: map[string]json.RawMessage{"durationMs": json.RawMessage("5000")},
				})
			}()
			synctest.Wait()
			cancel()
			<-done
			if err != nil || got.Output != nil || !errors.Is(got.ParentError, context.Canceled) ||
				!errors.Is(got.AttemptError, context.Canceled) || !errors.Is(got.HandlerError, context.Canceled) {
				t.Fatalf("parent cancellation lost: %+v, %v", got, err)
			}
		})
	})
	for _, tc := range []struct {
		name string
		make func(context.Context) (context.Context, context.CancelFunc)
		want error
	}{
		{"pre-cancelled", func(ctx context.Context) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			return ctx, cancel
		}, context.Canceled},
		{"already expired", func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithDeadline(ctx, time.Unix(1, 0))
		}, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			i := invoker(t, "android.tap", func(context.Context, registry.Invocation) (map[string]any, error) {
				called = true
				return map[string]any{}, nil
			})
			ctx, cancel := tc.make(t.Context())
			defer cancel()
			got, err := i.Invoke(ctx, node("android.tap", 1000), registry.Invocation{})
			var operation *xerror.OperationError
			if called || !errors.Is(err, tc.want) || errors.Is(err, xerror.ErrInvocationInvalid) ||
				!errors.As(err, &operation) || operation.Operation != "invoke_node_handler" || got.Output != nil || got.HandlerError != nil {
				t.Fatalf("inactive parent reached handler or lost context: %+v, %v, called=%v", got, err, called)
			}
		})
	}
}

func TestEachCallGetsFreshBudget(t *testing.T) {
	var deadlines []time.Time
	i := invoker(t, "flow.delay", func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
		deadline, _ := ctx.Deadline()
		deadlines = append(deadlines, deadline)
		return delay.Invoke(ctx, call)
	})
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		call := registry.Invocation{Inputs: map[string]json.RawMessage{"durationMs": json.RawMessage("500")}}
		for range 2 {
			got, err := i.Invoke(t.Context(), node("flow.delay", 1000), call)
			if err != nil || got.HandlerError != nil || got.AttemptError != nil || got.Output["durationMs"] != 500 {
				t.Fatalf("fresh attempt failed: %+v, %v", got, err)
			}
		}
		if !deadlines[0].Equal(start.Add(time.Second)) || !deadlines[1].Equal(start.Add(1500*time.Millisecond)) {
			t.Fatalf("attempt reused a deadline: %v", deadlines)
		}
	})
}

func TestDeadlineCheckedBeforeTimerSignal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		expired bool
	}{
		{name: "already expired parent", expired: true},
		{name: "parent expires during call"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			i := invoker(t, "android.tap", func(context.Context, registry.Invocation) (map[string]any, error) {
				called = true
				time.Sleep(time.Second)
				return map[string]any{}, nil
			})
			synctest.Test(t, func(t *testing.T) {
				deadline := time.Now().Add(time.Second)
				if tc.expired {
					deadline = time.Now()
				}
				parent := deadlineWithoutSignal{Context: t.Context(), deadline: deadline}
				got, err := i.Invoke(parent, node("android.tap", 2000), registry.Invocation{})
				if tc.expired {
					if called || !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("expired parent reached handler: %+v, %v", got, err)
					}
					return
				}
				if err != nil || !called || got.Output != nil || got.HandlerError != nil ||
					!errors.Is(got.ParentError, context.DeadlineExceeded) || !errors.Is(got.AttemptError, context.DeadlineExceeded) {
					t.Fatalf("deadline accepted before timer signal: %+v, %v", got, err)
				}
			})
		})
	}
}

// Models the interval between a deadline and observation of its timer signal.
type deadlineWithoutSignal struct {
	context.Context
	deadline time.Time
}

func (c deadlineWithoutSignal) Deadline() (time.Time, bool) { return c.deadline, true }

func TestConcurrentInvocationsRemainIsolated(t *testing.T) {
	t.Parallel()
	i := invoker(t, "android.tap", func(_ context.Context, call registry.Invocation) (map[string]any, error) {
		return map[string]any{"value": string(call.Inputs["value"])}, nil
	})
	for n := range 16 {
		t.Run(fmt.Sprintf("execution %d", n), func(t *testing.T) {
			t.Parallel()
			value := fmt.Sprint(n)
			got, err := i.Invoke(t.Context(), node("android.tap", 1000), registry.Invocation{
				Inputs: map[string]json.RawMessage{"value": json.RawMessage(value)},
			})
			if err != nil || got.HandlerError != nil || got.AttemptError != nil || got.ParentError != nil || got.Output["value"] != value {
				t.Fatalf("execution values leaked: %+v, %v", got, err)
			}
		})
	}
}
