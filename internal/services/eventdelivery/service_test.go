package eventdelivery

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

var stopDelivery = errors.New("test consumer stopped")

type readerStub struct {
	wakeup    <-chan struct{}
	register  func() <-chan struct{}
	watermark func(context.Context) (execution.EventStreamState, error)
	read      func(context.Context, int64, int) (execution.EventPage, error)
}

func (r *readerStub) EventWakeup() <-chan struct{} {
	if r.register != nil {
		return r.register()
	}
	return r.wakeup
}

func (r *readerStub) EventWatermark(ctx context.Context) (execution.EventStreamState, error) {
	return r.watermark(ctx)
}

func (r *readerStub) ReplayEvents(ctx context.Context, after int64, limit int) (execution.EventPage, error) {
	return r.read(ctx, after, limit)
}

type sinkStub struct {
	deliver func(context.Context, execution.Event) error
	idle    func(context.Context) error
}

func (s sinkStub) Deliver(ctx context.Context, event execution.Event) error {
	if s.deliver != nil {
		return s.deliver(ctx, event)
	}
	return nil
}

func (s sinkStub) Idle(ctx context.Context) error {
	if s.idle != nil {
		return s.idle(ctx)
	}
	return stopDelivery
}

func eventPage(cursors ...string) execution.EventPage {
	page := execution.EventPage{State: execution.EventStreamState{Watermark: 10}}
	for _, cursor := range cursors {
		page.Events = append(page.Events, execution.Event{Cursor: cursor})
	}
	return page
}

func TestPrepareCursorAndRegistrationOrder(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(strconv.FormatBool(explicit), func(t *testing.T) {
			var trace []string
			ctx := t.Context()
			reader := &readerStub{
				register: func() <-chan struct{} { trace = append(trace, "register"); return make(chan struct{}) },
				watermark: func(got context.Context) (execution.EventStreamState, error) {
					if got != ctx {
						t.Fatal("watermark lost caller context")
					}
					trace = append(trace, "watermark")
					return execution.EventStreamState{Watermark: 6}, nil
				},
				read: func(got context.Context, after int64, limit int) (execution.EventPage, error) {
					if got != ctx || limit != constants.MaxEventPageSize {
						t.Fatal("read lost context or page bound")
					}
					trace = append(trace, "read:"+strconv.FormatInt(after, 10))
					return eventPage(), nil
				},
			}
			after := int64(3)
			var input *int64
			want, wantTrace := int64(6), []string{"register", "watermark", "read:6"}
			if explicit {
				input, want, wantTrace = &after, 3, []string{"register", "read:3"}
			}
			session, err := New(reader).Prepare(ctx, input)
			if err != nil || !reflect.DeepEqual(trace, wantTrace) {
				t.Fatalf("prepare: %v, trace %v", err, trace)
			}
			after = 99 // The session must own its copied cursor.
			if cursor, err := session.Run(ctx, sinkStub{}); cursor != want || !errors.Is(err, stopDelivery) {
				t.Fatalf("copied cursor: %d, %v", cursor, err)
			}
			if session.page.Events != nil || session.wakeup != nil {
				t.Fatal("session retained prepared references")
			}
		})
	}
}

func TestPrepareRejectsInvalidInputAndReadFailures(t *testing.T) {
	for _, after := range []int64{-1, 0} {
		if session, err := (*Service)(nil).Prepare(t.Context(), &after); session != nil || !errors.Is(err, xerror.ErrExecutionInvalidInput) {
			t.Fatalf("invalid cursor %d: %v", after, err)
		}
	}
	after := int64(3)
	for _, service := range []*Service{nil, New(nil)} {
		if session, err := service.Prepare(t.Context(), &after); session != nil || !errors.Is(err, xerror.ErrExecutionPersistence) {
			t.Fatalf("missing dependency: %v", err)
		}
	}
	for _, cause := range []error{xerror.ErrEventCursorExpired, xerror.ErrEventCursorFuture, xerror.ErrExecutionPersistence} {
		t.Run(cause.Error(), func(t *testing.T) {
			reader := &readerStub{wakeup: make(chan struct{}), read: func(context.Context, int64, int) (execution.EventPage, error) {
				return execution.EventPage{}, cause
			}}
			if session, err := New(reader).Prepare(t.Context(), &after); session != nil || !errors.Is(err, cause) {
				t.Fatalf("prepare classification: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (*Service)(nil).Prepare(ctx, nil); err != context.Canceled {
		t.Fatalf("cancelled preparation: %v", err)
	}
	reader := &readerStub{wakeup: make(chan struct{}), watermark: func(context.Context) (execution.EventStreamState, error) {
		return execution.EventStreamState{}, stopDelivery
	}}
	if session, err := New(reader).Prepare(t.Context(), nil); session != nil || !errors.Is(err, stopDelivery) {
		t.Fatalf("watermark failure: %v", err)
	}
	reader.read = func(context.Context, int64, int) (execution.EventPage, error) {
		return execution.EventPage{More: true}, nil
	}
	if session, err := New(reader).Prepare(t.Context(), &after); session != nil || !errors.Is(err, xerror.ErrExecutionPersistence) {
		t.Fatalf("empty More page: %v", err)
	}
}

func TestPrepareIncludesCommitAfterWatermarkCapture(t *testing.T) {
	captured := false
	reader := &readerStub{wakeup: make(chan struct{}), watermark: func(context.Context) (execution.EventStreamState, error) {
		captured = true
		return execution.EventStreamState{Watermark: 3}, nil
	}, read: func(_ context.Context, after int64, _ int) (execution.EventPage, error) {
		if !captured || after != 3 {
			t.Fatal("connection-time watermark was lost")
		}
		return eventPage("4"), nil // Simulate a commit after watermark capture.
	}}
	session, err := New(reader).Prepare(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cursor, err := session.Run(t.Context(), sinkStub{}); cursor != 4 || !errors.Is(err, stopDelivery) {
		t.Fatalf("watermark/read race skipped new commit: %d, %v", cursor, err)
	}
}

func TestRunDependencyGuardsAndCancellationBoundary(t *testing.T) {
	for _, session := range []*Session{nil, {}} {
		if _, err := session.Run(t.Context(), sinkStub{}); !errors.Is(err, xerror.ErrExecutionPersistence) {
			t.Fatalf("unprepared session: %v", err)
		}
	}
	for _, cancelBefore := range []bool{false, true} {
		reader := &readerStub{wakeup: make(chan struct{}), read: func(context.Context, int64, int) (execution.EventPage, error) {
			return eventPage("4", "5"), nil
		}}
		after := int64(3)
		session, err := New(reader).Prepare(t.Context(), &after)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		calls, want := 0, int64(4)
		if cancelBefore {
			cancel()
			want = 3
		}
		cursor, err := session.Run(ctx, sinkStub{deliver: func(context.Context, execution.Event) error {
			calls++
			cancel()
			return nil
		}})
		cancel()
		if cursor != want || !errors.Is(err, context.Canceled) || calls != int(want-3) {
			t.Fatalf("cancellation boundary: %d, %v, calls=%d", cursor, err, calls)
		}
	}
}

func TestRunCursorGuardsAndDeliveryFailure(t *testing.T) {
	for _, test := range []struct {
		name    string
		cursors []string
		failAt  string
		want    int64
		cause   error
	}{
		{"gaps", []string{"4", "6", "10"}, "", 10, stopDelivery},
		{"malformed", []string{"4", "bad"}, "", 4, xerror.ErrExecutionPersistence},
		{"backwards", []string{"4", "3"}, "", 4, xerror.ErrExecutionPersistence},
		{"repeated", []string{"4", "4"}, "", 4, xerror.ErrExecutionPersistence},
		{"future", []string{"4", "11"}, "", 4, xerror.ErrExecutionPersistence},
		{"failed send", []string{"4", "5", "6"}, "5", 4, stopDelivery},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads, calls := 0, 0
			reader := &readerStub{wakeup: make(chan struct{}), read: func(context.Context, int64, int) (execution.EventPage, error) {
				reads++
				return eventPage(test.cursors...), nil
			}}
			after := int64(3)
			session, err := New(reader).Prepare(t.Context(), &after)
			if err != nil {
				t.Fatal(err)
			}
			cursor, err := session.Run(t.Context(), sinkStub{deliver: func(_ context.Context, event execution.Event) error {
				calls++
				if event.Cursor == test.failAt {
					return stopDelivery
				}
				return nil
			}})
			if cursor != test.want || !errors.Is(err, test.cause) || reads != 1 || (test.failAt != "" && calls != 2) {
				t.Fatalf("delivery: cursor=%d err=%v reads=%d calls=%d", cursor, err, reads, calls)
			}
		})
	}
}

func TestRunFallbackAndLaterReadFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reads, idles := 0, 0
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		reader := &readerStub{wakeup: make(chan struct{}), watermark: func(context.Context) (execution.EventStreamState, error) {
			return execution.EventStreamState{}, nil
		}, read: func(_ context.Context, after int64, _ int) (execution.EventPage, error) {
			reads++
			if reads == 3 {
				return eventPage("1"), nil
			}
			if after != 0 {
				t.Fatalf("idle advanced cursor: %d", after)
			}
			return eventPage(), nil
		}}
		session, err := New(reader).Prepare(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		cursor, err := session.Run(ctx, sinkStub{
			deliver: func(context.Context, execution.Event) error { cancel(); return nil },
			idle:    func(context.Context) error { idles++; return nil },
		})
		if cursor != 1 || err != context.Canceled || reads != 3 || idles != 2 || time.Since(started) != 2*constants.EventObservationRecheckInterval {
			t.Fatalf("fallback: cursor=%d err=%v reads=%d idles=%d elapsed=%v", cursor, err, reads, idles, time.Since(started))
		}
	})
	closed := make(chan struct{})
	close(closed)
	reads := 0
	reader := &readerStub{wakeup: closed, read: func(context.Context, int64, int) (execution.EventPage, error) {
		reads++
		if reads == 1 {
			return eventPage("4"), nil
		}
		return execution.EventPage{}, xerror.ErrEventCursorExpired
	}}
	after := int64(3)
	session, err := New(reader).Prepare(t.Context(), &after)
	if err != nil {
		t.Fatal(err)
	}
	if cursor, err := session.Run(t.Context(), sinkStub{idle: func(context.Context) error { return nil }}); cursor != 4 || !errors.Is(err, xerror.ErrEventCursorExpired) || reads != 2 {
		t.Fatalf("later expiry: %d, %v, reads=%d", cursor, err, reads)
	}
}

func TestRunSingleUseAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader := &readerStub{wakeup: make(chan struct{}), read: func(context.Context, int64, int) (execution.EventPage, error) { return eventPage(), nil }}
		after := int64(3)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		session, err := New(reader).Prepare(ctx, &after)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			cursor, err := session.Run(ctx, sinkStub{idle: func(context.Context) error { return nil }})
			if cursor != 3 {
				done <- errors.New("cancellation changed cursor")
				return
			}
			done <- err
		}()
		synctest.Wait()
		if cursor, err := session.Run(t.Context(), sinkStub{}); cursor != 0 || !errors.Is(err, xerror.ErrExecutionStaleState) {
			t.Fatalf("concurrent run: %d, %v", cursor, err)
		}
		cancel()
		if err := <-done; err != context.Canceled {
			t.Fatalf("cancelled wait: %v", err)
		}
		if _, err := session.Run(t.Context(), sinkStub{}); !errors.Is(err, xerror.ErrExecutionStaleState) {
			t.Fatalf("reused session: %v", err)
		}
	})
}
