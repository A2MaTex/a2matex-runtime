package delay

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestInvokeCompletesAfterRequestedDuration(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		want      int
	}{
		{"zero", "0", 0},
		{"integer number", "1500.0", 1500},
		{"maximum", "300000", constants.MaxFlowDelayMS},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				duration := time.Duration(test.want) * time.Millisecond
				ctx, cancel := context.WithTimeout(t.Context(), duration+time.Second)
				defer cancel()
				started := time.Now()
				output, err := Invoke(ctx, delayInvocation(test.raw))
				if err != nil || len(output) != 1 || output["durationMs"] != test.want || time.Since(started) != duration {
					t.Fatalf("output = %#v, error = %v, elapsed = %v, want %v", output, err, time.Since(started), duration)
				}
			})
		})
	}
}

func TestInvokeRejectsInvalidInputsWithoutWaiting(t *testing.T) {
	for _, test := range []struct {
		name, rawInputs string
		config          map[string]json.RawMessage
	}{
		{"missing", "{}", nil},
		{"unexpected field", `{"durationMs":5,"extra":0}`, nil},
		{"unexpected config", `{"durationMs":5}`, map[string]json.RawMessage{"extra": json.RawMessage("1")}},
		{"null", `{"durationMs":null}`, nil},
		{"fractional", `{"durationMs":1.5}`, nil},
		{"negative", `{"durationMs":-1}`, nil},
		{"above maximum", `{"durationMs":300001}`, nil},
		{"quoted", `{"durationMs":"5"}`, nil},
		{"boolean", `{"durationMs":true}`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var inputs map[string]json.RawMessage
				if err := json.Unmarshal([]byte(test.rawInputs), &inputs); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				started := time.Now()
				output, err := Invoke(ctx, registry.Invocation{Config: test.config, Inputs: inputs})
				var failure xerror.CommandError
				if output != nil || !errors.As(err, &failure) || failure.Code != constants.CommandInputUnsupported ||
					failure.Retryable || time.Since(started) != 0 {
					t.Fatalf("output = %#v, error = %#v, elapsed = %v", output, err, time.Since(started))
				}
			})
		})
	}
}

func TestInvokeHonorsDeadlineDuringWait(t *testing.T) {
	for _, test := range []struct {
		name, requested string
	}{
		{"shorter deadline", "1500"},
		{"timer and deadline tie", "1000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				started := time.Now()
				output, err := Invoke(ctx, delayInvocation(test.requested))
				if output != nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != time.Second {
					t.Fatalf("output = %#v, error = %v, elapsed = %v", output, err, time.Since(started))
				}
			})
		})
	}
}

func TestInvokeCancelsDuringWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		type outcome struct {
			output map[string]any
			err    error
		}
		result := make(chan outcome, 1)
		go func() {
			output, err := Invoke(ctx, delayInvocation("1000"))
			result <- outcome{output: output, err: err}
		}()
		synctest.Wait()
		cancel()
		got := <-result
		if got.output != nil || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("output = %#v, error = %v, want cancellation", got.output, got.err)
		}
	})
}

func delayInvocation(raw string) registry.Invocation {
	return registry.Invocation{Inputs: map[string]json.RawMessage{"durationMs": json.RawMessage(raw)}}
}
