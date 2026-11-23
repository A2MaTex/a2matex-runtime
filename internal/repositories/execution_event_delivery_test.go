package repositories

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/services/eventdelivery"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

var stopEventDelivery = errors.New("test delivery complete")

type eventSink struct {
	deliver func(context.Context, execution.Event) error
	idle    func(context.Context) error
}

func (s eventSink) Deliver(ctx context.Context, event execution.Event) error {
	return s.deliver(ctx, event)
}

func (s eventSink) Idle(ctx context.Context) error {
	if s.idle != nil {
		return s.idle(ctx)
	}
	return stopEventDelivery
}

type deliveryResult struct {
	cursor int64
	err    error
}

func appendDeliveryEvents(t *testing.T, repo *ExecutionRepository, admission execution.Admission, count int) {
	t.Helper()
	events := make([]execution.PendingEvent, count)
	for index := range events {
		events[index] = groupEvent(admission)
	}
	if err := repo.WithWriteTx(t.Context(), func(tx Tx) error {
		_, err := execution.AppendEventBatches(t.Context(), tx, events)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEventDeliveryReplayAndNewCommits(t *testing.T) {
	repo, admission := admittedEventStore(t)
	appendDeliveryEvents(t, repo, admission, 205)
	service := eventdelivery.New(repo)
	after := int64(3)
	session, err := service.Prepare(t.Context(), &after)
	if err != nil {
		t.Fatal(err)
	}
	count, idles := 0, 0
	cursor, err := session.Run(t.Context(), eventSink{
		deliver: func(_ context.Context, event execution.Event) error {
			count++
			if event.Cursor != strconv.Itoa(count+3) || event.RunGroupID != admission.RunGroupID || string(event.Payload) != `{"status":"RUNNING"}` {
				t.Fatalf("out of order or changed event: %+v at %d", event, count)
			}
			if count == 1 {
				// The prepared read transaction must be released during delivery.
				appendDeliveryEvents(t, repo, admission, 1)
			}
			return nil
		},
		idle: func(context.Context) error {
			idles++
			if count != 206 {
				t.Fatalf("waited before draining replay: %d", count)
			}
			return stopEventDelivery
		},
	})
	if !errors.Is(err, stopEventDelivery) || cursor != 209 || count != 206 || idles != 1 {
		t.Fatalf("replay/live: %d, %v, count=%d idles=%d", cursor, err, count, idles)
	}
	state, err := repo.EventWatermark(t.Context())
	if err != nil || state.Watermark != 209 {
		t.Fatalf("consumption changed event state: %+v, %v", state, err)
	}
	// A failed send stays available to a new consumer at its prior cursor.
	failed, err := service.Prepare(t.Context(), &after)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err = failed.Run(t.Context(), eventSink{deliver: func(_ context.Context, event execution.Event) error {
		if event.Cursor == "5" {
			return stopEventDelivery
		}
		return nil
	}})
	if cursor != 4 || !errors.Is(err, stopEventDelivery) {
		t.Fatalf("failed send advanced position: %d, %v", cursor, err)
	}
	resumed, err := service.Prepare(t.Context(), &cursor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resumed.Run(t.Context(), eventSink{deliver: func(_ context.Context, event execution.Event) error {
		if event.Cursor != "5" {
			t.Fatalf("reconnect skipped failed delivery: %s", event.Cursor)
		}
		return stopEventDelivery
	}})
	if !errors.Is(err, stopEventDelivery) {
		t.Fatal(err)
	}
}

func TestEventDeliveryEmptyStartAndIdleHandoff(t *testing.T) {
	repo := executionStore(t)
	service := eventdelivery.New(repo)
	session, err := service.Prepare(t.Context(), nil) // Fresh internal cursor zero.
	if err != nil {
		t.Fatal(err)
	}
	admission := testAdmission(0)
	if _, err := repo.Admit(t.Context(), admission); err != nil {
		t.Fatal(err)
	}
	count := 0
	cursor, err := session.Run(t.Context(), eventSink{deliver: func(_ context.Context, event execution.Event) error {
		count++
		if event.Cursor != strconv.Itoa(count) {
			t.Fatalf("empty start skipped first commit: %s", event.Cursor)
		}
		return nil
	}, idle: func(context.Context) error {
		if count == 3 {
			return stopEventDelivery
		}
		return nil
	}})
	if cursor != 3 || !errors.Is(err, stopEventDelivery) {
		t.Fatalf("fresh stream: %d, %v", cursor, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	session, err = service.Prepare(ctx, nil) // Skip the existing three events.
	if err != nil {
		t.Fatal(err)
	}
	ready, release, done := make(chan struct{}), make(chan struct{}), make(chan deliveryResult, 1)
	go func() {
		idles := 0
		cursor, err := session.Run(ctx, eventSink{
			deliver: func(_ context.Context, event execution.Event) error {
				if event.Cursor != "4" {
					return errors.New("omitted cursor replayed history")
				}
				return nil
			},
			idle: func(ctx context.Context) error {
				idles++
				if idles > 1 {
					return stopEventDelivery
				}
				close(ready)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
		})
		done <- deliveryResult{cursor, err}
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	appendDeliveryEvents(t, repo, admission, 1) // After empty read, before wait.
	close(release)
	result := <-done
	if result.cursor != 4 || !errors.Is(result.err, stopEventDelivery) {
		t.Fatalf("idle handoff: %+v", result)
	}
}

func TestEventDeliveryRetention(t *testing.T) {
	repo, admission := admittedEventStore(t)
	appendDeliveryEvents(t, repo, admission, 205)
	service := eventdelivery.New(repo)
	after := int64(3)
	session, err := service.Prepare(t.Context(), &after)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PruneEvents(t.Context(), 1, 512); err != nil {
		t.Fatal(err)
	}
	count := 0
	cursor, err := session.Run(t.Context(), eventSink{deliver: func(context.Context, execution.Event) error { count++; return nil }})
	if count != 100 || cursor != 103 || !errors.Is(err, xerror.ErrEventCursorExpired) {
		t.Fatalf("expiry during delivery: count=%d cursor=%d err=%v", count, cursor, err)
	}
	if session, err := service.Prepare(t.Context(), &after); session != nil || !errors.Is(err, xerror.ErrEventCursorExpired) {
		t.Fatalf("pre-stream expiry: %v", err)
	}
	after = 207 // Exactly the replay floor is allowed.
	session, err = service.Prepare(t.Context(), &after)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err = session.Run(t.Context(), eventSink{deliver: func(context.Context, execution.Event) error { return nil }})
	if cursor != 208 || !errors.Is(err, stopEventDelivery) {
		t.Fatalf("floor equality: %d, %v", cursor, err)
	}
}

func TestEventDeliveryIndependentConsumers(t *testing.T) {
	repo, admission := admittedEventStore(t)
	appendDeliveryEvents(t, repo, admission, 1)
	service, after := eventdelivery.New(repo), int64(3)
	slow, err := service.Prepare(t.Context(), &after)
	if err != nil {
		t.Fatal(err)
	}
	fast, err := service.Prepare(t.Context(), &after)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered, done := make(chan struct{}), make(chan deliveryResult, 1)
	go func() {
		cursor, err := slow.Run(ctx, eventSink{deliver: func(ctx context.Context, _ execution.Event) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}})
		done <- deliveryResult{cursor, err}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	appendDeliveryEvents(t, repo, admission, 1) // Must not block on the slow sink.
	fastCtx, stopFast := context.WithCancel(ctx)
	defer stopFast()
	count := 0
	cursor, err := fast.Run(fastCtx, eventSink{deliver: func(_ context.Context, event execution.Event) error {
		count++
		if event.Cursor == "5" {
			stopFast()
		}
		return nil
	}, idle: func(context.Context) error { return nil }})
	if cursor != 5 || count != 2 || !errors.Is(err, context.Canceled) {
		t.Fatalf("independent fast consumer: %d, %v, count=%d", cursor, err, count)
	}
	cancel()
	result := <-done
	if result.cursor != 3 || !errors.Is(result.err, context.Canceled) {
		t.Fatalf("slow consumer cleanup: %+v", result)
	}
}
