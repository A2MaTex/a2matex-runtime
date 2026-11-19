package variables

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

func fixture(t *testing.T) (*Store, string, string) {
	t.Helper()
	a, b := uuid.NewString(), uuid.NewString()
	s, err := New(uuid.NewString(), []string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	return s, a, b
}

func candidate(s *Store, device string, scope constants.VariableScope, name, value string) execution.VariableWrite {
	return execution.VariableWrite{AttemptRef: execution.AttemptRef{RunGroupID: s.groupID, DeviceExecutionID: device,
		AttemptID: uuid.NewString(), NodeID: "set_value"}, Scope: scope, Name: name, Value: []byte(value)}
}

func committed(context.Context) (execution.CheckpointResult, error) {
	return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "1"}, nil
}

func apply(t *testing.T, s *Store, device string, scope constants.VariableScope, name, value string) {
	t.Helper()
	if _, err := s.Apply(t.Context(), candidate(s, device, scope, name, value), committed); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, s *Store, device string, scope constants.VariableScope, name, want string, present bool) {
	t.Helper()
	got, found, err := s.Get(t.Context(), device, scope, name)
	if err != nil || found != present || string(got) != want {
		t.Fatalf("%s: %s present=%v error=%v", name, got, found, err)
	}
}

func TestNewOwnershipAndScalarCopies(t *testing.T) {
	s, a, b := fixture(t)
	for _, tc := range []struct{ name, value string }{{"score", "30"}, {"zero", "0"}, {"flag", "false"}, {"empty", `""`}} {
		apply(t, s, a, constants.VariableScopeFlow, tc.name, tc.value)
		read(t, s, a, constants.VariableScopeFlow, tc.name, tc.value, true)
		read(t, s, b, constants.VariableScopeFlow, tc.name, "", false)
	}
	apply(t, s, b, constants.VariableScopeFlow, "score", "75")
	apply(t, s, a, constants.VariableScopeRunGroup, "bestScore", "75")
	apply(t, s, b, constants.VariableScopeRunGroup, "bestScore", "80")
	read(t, s, a, constants.VariableScopeRunGroup, "bestScore", "80", true)
	read(t, s, b, constants.VariableScopeRunGroup, "bestScore", "80", true)
	apply(t, s, a, constants.VariableScopeFlow, "Score", "true")
	read(t, s, a, constants.VariableScopeFlow, "score", "30", true)
	write := candidate(s, a, constants.VariableScopeFlow, "owned", `"before"`)
	_, err := s.Apply(t.Context(), write, func(ctx context.Context) (execution.CheckpointResult, error) {
		copy(write.Value, []byte(`"after!"`)) // Candidate was copied before checkpoint entry.
		return committed(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Get(t.Context(), a, constants.VariableScopeFlow, "owned")
	if err != nil {
		t.Fatal(err)
	}
	got[1] = 'X'
	read(t, s, a, constants.VariableScopeFlow, "owned", `"before"`, true)
	apply(t, s, a, constants.VariableScopeFlow, "owned", "false")
	read(t, s, a, constants.VariableScopeFlow, "owned", "false", true)
	other, _, _ := fixture(t)
	if _, _, err := other.Get(t.Context(), a, constants.VariableScopeRunGroup, "bestScore"); !errors.Is(err, xerror.ErrVariableInvalid) {
		t.Fatal("foreign device admitted", err)
	}
}

func TestIdentityAndUnavailableGuards(t *testing.T) {
	group, a := uuid.NewString(), uuid.NewString()
	for _, tc := range []struct {
		name, group string
		devices     []string
	}{
		{"group", "bad", []string{a}}, {"empty", group, nil}, {"invalid device", group, []string{"bad"}},
		{"duplicate", group, []string{a, a}}, {"too many", group, make([]string, constants.MaxAdmissionDevices+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if s, err := New(tc.group, tc.devices); s != nil || !errors.Is(err, xerror.ErrVariableInvalid) {
				t.Fatalf("%v %v", s, err)
			}
		})
	}
	ids := []string{a}
	s, err := New(group, ids)
	if err != nil {
		t.Fatal(err)
	}
	ids[0] = uuid.NewString()
	read(t, s, a, constants.VariableScopeFlow, "missing", "", false)
	for _, change := range []func(*execution.VariableWrite){
		func(w *execution.VariableWrite) { w.RunGroupID = uuid.NewString() },
		func(w *execution.VariableWrite) { w.DeviceExecutionID = uuid.NewString() },
		func(w *execution.VariableWrite) { w.AttemptID = "bad" },
		func(w *execution.VariableWrite) { w.NodeID = "bad node" },
		func(w *execution.VariableWrite) { w.Scope = "GLOBAL" },
		func(w *execution.VariableWrite) { w.Name = "bad name" },
	} {
		w := candidate(s, a, constants.VariableScopeFlow, "score", "1")
		change(&w)
		_, err := s.Apply(t.Context(), w, func(context.Context) (execution.CheckpointResult, error) {
			t.Fatal("invalid candidate reached checkpoint")
			return execution.CheckpointResult{}, nil
		})
		if !errors.Is(err, xerror.ErrVariableInvalid) {
			t.Fatal(err)
		}
	}
	if _, err := s.Apply(t.Context(), candidate(s, a, constants.VariableScopeFlow, "score", "1"), nil); !errors.Is(err, xerror.ErrVariableInvalid) {
		t.Fatal(err)
	}
	for _, device := range []string{a, uuid.NewString()} {
		if _, _, err := s.Get(t.Context(), device, "GLOBAL", "score"); !errors.Is(err, xerror.ErrVariableInvalid) {
			t.Fatal(err)
		}
	}
	for _, zero := range []*Store{nil, {}} {
		if _, _, err := zero.Get(t.Context(), a, constants.VariableScopeFlow, "score"); !errors.Is(err, xerror.ErrVariableInvalid) {
			t.Fatal(err)
		}
		if err := zero.Close(t.Context()); !errors.Is(err, xerror.ErrVariableInvalid) {
			t.Fatal(err)
		}
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal("close not idempotent", err)
	}
	if _, _, err := s.Get(t.Context(), a, constants.VariableScopeFlow, "score"); !errors.Is(err, xerror.ErrVariableUnavailable) {
		t.Fatal(err)
	}
	if _, err := s.Apply(t.Context(), candidate(s, a, constants.VariableScopeFlow, "score", "1"), committed); !errors.Is(err, xerror.ErrVariableUnavailable) {
		t.Fatal(err)
	}
	if s.flows != nil || s.global.values != nil || s.global.bytes != 0 {
		t.Fatal("Close retained scope allocations")
	}
}

func TestQuotasAndReplacement(t *testing.T) {
	t.Run("name limit", func(t *testing.T) {
		s, a, b := fixture(t)
		for i := range constants.MaxVariablesPerScope {
			apply(t, s, a, constants.VariableScopeFlow, fmt.Sprintf("v%d", i), "0")
		}
		apply(t, s, a, constants.VariableScopeFlow, "v0", `"replaced"`)
		calls := 0
		_, err := s.Apply(t.Context(), candidate(s, a, constants.VariableScopeFlow, "extra", "0"), func(ctx context.Context) (execution.CheckpointResult, error) { calls++; return committed(ctx) })
		var command xerror.CommandError
		if !errors.As(err, &command) || command.Code != constants.CommandInputUnsupported || command.Retryable || calls != 0 {
			t.Fatal("name quota", err, calls)
		}
		read(t, s, a, constants.VariableScopeFlow, "v0", `"replaced"`, true)
		apply(t, s, b, constants.VariableScopeFlow, "extra", "0")
		apply(t, s, a, constants.VariableScopeRunGroup, "extra", "0")
	})
	t.Run("byte limit", func(t *testing.T) {
		s, a, _ := fixture(t)
		used := 0
		for i := range 15 {
			name := fmt.Sprintf("v%02d", i)
			value := `"` + strings.Repeat("x", constants.MaxVariableValueBytes-2) + `"`
			apply(t, s, a, constants.VariableScopeFlow, name, value)
			used += len(name) + len(value)
		}
		remaining := constants.MaxVariableScopeBytes - used - len("last")
		value := `"` + strings.Repeat("x", remaining-2) + `"`
		apply(t, s, a, constants.VariableScopeFlow, "last", value)
		for _, w := range []execution.VariableWrite{candidate(s, a, constants.VariableScopeFlow, "last", `"`+strings.Repeat("x", remaining-1)+`"`), candidate(s, a, constants.VariableScopeFlow, "extra", "0"), candidate(s, a, constants.VariableScopeFlow, "last", "null")} {
			_, err := s.Apply(t.Context(), w, func(context.Context) (execution.CheckpointResult, error) {
				t.Fatal("over quota/invalid value reached checkpoint")
				return execution.CheckpointResult{}, nil
			})
			var command xerror.CommandError
			if !errors.As(err, &command) || command.Code != constants.CommandInputUnsupported {
				t.Fatal(err)
			}
		}
		read(t, s, a, constants.VariableScopeFlow, "last", value, true)
		apply(t, s, a, constants.VariableScopeFlow, "v00", "0")
		apply(t, s, a, constants.VariableScopeFlow, "extra", "0")
	})
}

func TestCheckpointConfirmationAndFencing(t *testing.T) {
	for _, disposition := range []constants.CheckpointDisposition{constants.CheckpointAlreadyFinalized, constants.CheckpointStale} {
		s, a, _ := fixture(t)
		apply(t, s, a, constants.VariableScopeFlow, "score", "80")
		want := execution.CheckpointResult{Disposition: disposition}
		got, err := s.Apply(t.Context(), candidate(s, a, constants.VariableScopeFlow, "score", "30"), func(context.Context) (execution.CheckpointResult, error) { return want, nil })
		if err != nil || got != want {
			t.Fatal(got, err)
		}
		read(t, s, a, constants.VariableScopeFlow, "score", "80", true)
	}
	for _, tc := range []struct {
		name    string
		result  execution.CheckpointResult
		failure error
	}{
		{"rollback", execution.CheckpointResult{}, xerror.ErrExecutionPersistence},
		{"unknown commit", execution.CheckpointResult{}, xerror.ErrExecutionCommitUnknown},
		{"nil is not confirmation", execution.CheckpointResult{}, nil},
		{"bad sequence", execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Cursor: "1"}, nil},
		{"bad cursor", execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "0"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a, _ := fixture(t)
			calls := 0
			result, err := s.Apply(t.Context(), candidate(s, a, constants.VariableScopeFlow, "score", "30"), func(context.Context) (execution.CheckpointResult, error) { calls++; return tc.result, tc.failure })
			want := tc.failure
			if want == nil {
				want = xerror.ErrVariableInvalid
			}
			if !errors.Is(err, want) || calls != 1 || result != (execution.CheckpointResult{}) {
				t.Fatal(result, err, calls)
			}
			if _, _, err := s.Get(t.Context(), a, constants.VariableScopeFlow, "score"); !errors.Is(err, xerror.ErrVariableUnavailable) {
				t.Fatal(err)
			}
			if _, err := s.Apply(t.Context(), candidate(s, a, constants.VariableScopeFlow, "score", "75"), committed); !errors.Is(err, xerror.ErrVariableUnavailable) {
				t.Fatal(err)
			}
			if err := s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPublicationCancellationAndCloseOrdering(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, a, b := fixture(t)
		entered, release, applied := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go func() {
			_, err := s.Apply(ctx, candidate(s, a, constants.VariableScopeRunGroup, "score", "75"), func(ctx context.Context) (execution.CheckpointResult, error) {
				close(entered)
				<-release
				cancel()
				return committed(ctx)
			})
			applied <- err
		}()
		<-entered
		readDone := make(chan json.RawMessage, 1)
		go func() {
			value, _, err := s.Get(t.Context(), b, constants.VariableScopeRunGroup, "score")
			if err != nil {
				t.Error(err)
			}
			readDone <- value
		}()
		synctest.Wait()
		select {
		case <-readDone:
			t.Fatal("reader observed an unpublished candidate")
		default:
		}
		waitCtx, stop := context.WithCancel(t.Context())
		stop()
		if _, _, err := s.Get(waitCtx, b, constants.VariableScopeRunGroup, "score"); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if err := s.Close(waitCtx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if _, err := s.Apply(waitCtx, candidate(s, b, constants.VariableScopeFlow, "value", "1"), func(context.Context) (execution.CheckpointResult, error) {
			t.Fatal("cancelled apply checkpointed")
			return execution.CheckpointResult{}, nil
		}); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		close(release)
		if err := <-applied; err != nil {
			t.Fatal("late cancellation overrode commit", err)
		}
		if value := <-readDone; string(value) != "75" {
			t.Fatalf("published %s", value)
		}
		// Reusing the same store models live Pause/Resume; no lifecycle API is claimed.
		read(t, s, a, constants.VariableScopeRunGroup, "score", "75", true)
		closeEntered, closeRelease := make(chan struct{}), make(chan struct{})
		go func() {
			_, err := s.Apply(t.Context(), candidate(s, a, constants.VariableScopeFlow, "last", "1"), func(ctx context.Context) (execution.CheckpointResult, error) {
				close(closeEntered)
				<-closeRelease
				return committed(ctx)
			})
			applied <- err
		}()
		<-closeEntered
		closed := make(chan error, 1)
		go func() { closed <- s.Close(t.Context()) }()
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("Close raced through active publication")
		default:
		}
		close(closeRelease)
		if err := <-applied; err != nil {
			t.Fatal(err)
		}
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
		fresh, err := New(uuid.NewString(), []string{a})
		if err != nil {
			t.Fatal(err)
		}
		read(t, fresh, a, constants.VariableScopeRunGroup, "score", "", false)
	})
	s, a, _ := fixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if _, _, err := s.Get(ctx, a, constants.VariableScopeFlow, "score"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestConcurrentGlobalWritesAndFinalNameSlot(t *testing.T) {
	s, a, b := fixture(t)
	var order atomic.Int64
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			device := a
			if i%2 == 1 {
				device = b
			}
			value := fmt.Sprint(i)
			_, err := s.Apply(t.Context(), candidate(s, device, constants.VariableScopeRunGroup, "score", value), func(context.Context) (execution.CheckpointResult, error) {
				order.Store(int64(i))
				return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "1"}, nil
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	read(t, s, a, constants.VariableScopeRunGroup, "score", fmt.Sprint(order.Load()), true)
	for i := range constants.MaxVariablesPerScope - 2 {
		apply(t, s, a, constants.VariableScopeRunGroup, fmt.Sprintf("v%d", i), "0")
	}
	var successful, callbacks atomic.Int32
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Apply(t.Context(), candidate(s, b, constants.VariableScopeRunGroup, fmt.Sprintf("last%d", i), "0"), func(ctx context.Context) (execution.CheckpointResult, error) { callbacks.Add(1); return committed(ctx) })
			if err == nil {
				successful.Add(1)
			} else {
				var command xerror.CommandError
				if !errors.As(err, &command) {
					t.Error(err)
				}
			}
		}(i)
	}
	wg.Wait()
	if successful.Load() != 1 || callbacks.Load() != 1 {
		t.Fatal("concurrent writers exceeded final slot", successful.Load(), callbacks.Load())
	}
}
