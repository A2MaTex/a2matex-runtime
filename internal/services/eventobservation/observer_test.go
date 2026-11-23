package eventobservation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type sourceFunc func() <-chan struct{}

func (f sourceFunc) EventWakeup() <-chan struct{} { return f() }

func TestRegisterCapturesChannelAndRechecksCancellation(t *testing.T) {
	wakeup := make(chan struct{})
	calls := 0
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	observer := New(sourceFunc(func() <-chan struct{} { calls++; return wakeup }))
	if calls != 0 {
		t.Fatal("constructor subscribed")
	}
	if got, err := observer.Register(ctx); err != nil || got != wakeup || calls != 1 {
		t.Fatalf("registration: %v, %v, calls=%d", got, err, calls)
	}
	cancel()
	if got, err := observer.Register(ctx); got != nil || err != context.Canceled || calls != 1 {
		t.Fatalf("cancelled registration: %v, %v, calls=%d", got, err, calls)
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	observer = New(sourceFunc(func() <-chan struct{} { cancel(); return nil }))
	if got, err := observer.Register(ctx); got != nil || err != context.Canceled {
		t.Fatalf("cancellation during registration: %v, %v", got, err)
	}
}

func TestObservationRejectsMissingDependencies(t *testing.T) {
	wakeup := make(chan struct{})
	for _, test := range []struct {
		name     string
		observer *Observer
		wakeup   <-chan struct{}
	}{
		{"missing receiver", nil, wakeup},
		{"missing source", New(nil), wakeup},
		{"missing channel", New(sourceFunc(func() <-chan struct{} { return nil })), nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.observer.Register(t.Context())
			requireObservationFailure(t, err, "register_event_observation")
			if got != nil {
				t.Fatal("invalid registration returned a channel")
			}
			requireObservationFailure(t, test.observer.Wait(t.Context(), test.wakeup), "wait_event_observation")
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := test.observer.Register(ctx); err != context.Canceled {
				t.Fatalf("registration cancellation lost: %v", err)
			}
			if err := test.observer.Wait(ctx, test.wakeup); err != context.Canceled {
				t.Fatalf("wait cancellation lost: %v", err)
			}
		})
	}
	observer := New(sourceFunc(func() <-chan struct{} { return wakeup }))
	requireObservationFailure(t, observer.Wait(t.Context(), nil), "wait_event_observation")
}

func TestWaitBroadcastAndIndependentCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wakeup := make(chan struct{})
		observer := New(sourceFunc(func() <-chan struct{} { return wakeup }))
		parent, stop := context.WithCancel(t.Context())
		defer stop()
		first, cancel := context.WithCancel(parent)
		defer cancel()
		results := []chan error{make(chan error, 1), make(chan error, 1), make(chan error, 1)}
		for i, ctx := range []context.Context{first, parent, parent} {
			registered, err := observer.Register(ctx)
			if err != nil || registered != wakeup {
				t.Fatalf("registration: %v, %v", registered, err)
			}
			go func() { results[i] <- observer.Wait(ctx, registered) }()
		}
		synctest.Wait()
		for _, result := range results {
			select {
			case err := <-result:
				t.Fatalf("open channel released before notification: %v", err)
			default:
			}
		}
		cancel()
		if err := <-results[0]; err != context.Canceled {
			t.Fatalf("independent cancellation: %v", err)
		}
		close(wakeup)
		for _, result := range results[1:] {
			if err := <-result; err != nil {
				t.Fatalf("broadcast: %v", err)
			}
		}
	})
}

func TestWaitFallbackAndDeadline(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "fallback"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				wakeup := make(chan struct{})
				observer := New(sourceFunc(func() <-chan struct{} {
					t.Fatal("waiting must not register again")
					return nil
				}))
				ctx := t.Context()
				wantDuration := constants.EventObservationRecheckInterval
				var wantError error
				if deadline {
					wantDuration /= 2
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, wantDuration)
					defer cancel()
					wantError = context.DeadlineExceeded
				}
				started := time.Now()
				if err := observer.Wait(ctx, wakeup); err != wantError || time.Since(started) != wantDuration {
					t.Fatalf("wait: %v after %v; want %v after %v", err, time.Since(started), wantError, wantDuration)
				}
				select {
				case <-wakeup:
					t.Fatal("wait closed repository channel")
				default:
				}
			})
		})
	}
}

func TestWaitParentStopAndRepeatedCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		wakeup := make(chan struct{})
		observer := New(sourceFunc(func() <-chan struct{} { return wakeup }))
		for range 4 {
			ctx, cancel := context.WithCancel(t.Context())
			results := make(chan error, 16)
			for range cap(results) {
				go func() { results <- observer.Wait(ctx, wakeup) }()
			}
			synctest.Wait()
			cancel()
			for range cap(results) {
				if err := <-results; err != context.Canceled {
					t.Fatalf("parent cancellation: %v", err)
				}
			}
		}
	})
}

func TestWaitCommitCancellationRace(t *testing.T) {
	for range 32 {
		wakeup := make(chan struct{})
		observer := New(sourceFunc(func() <-chan struct{} { return wakeup }))
		ctx, cancel := context.WithCancel(t.Context())
		start := make(chan struct{})
		var workers sync.WaitGroup
		workers.Go(func() { <-start; cancel() })
		workers.Go(func() { <-start; close(wakeup) })
		close(start)
		err := observer.Wait(ctx, wakeup)
		workers.Wait()
		if err != nil && err != context.Canceled {
			t.Fatalf("commit/cancel race: %v", err)
		}
		if err := observer.Wait(ctx, wakeup); err != context.Canceled {
			t.Fatalf("cancelled context lost to closed channel: %v", err)
		}
	}
}

func requireObservationFailure(t *testing.T, err error, operation string) {
	t.Helper()
	var failure *xerror.OperationError
	if !errors.Is(err, xerror.ErrExecutionPersistence) || !errors.As(err, &failure) || failure.Operation != operation {
		t.Fatalf("got %v; want persistence failure at %s", err, operation)
	}
}
