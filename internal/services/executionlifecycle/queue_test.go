package executionlifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

func queueMembers(count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	return ids
}

func TestDeviceQueueFIFOAndRelease(t *testing.T) {
	ids := queueMembers(3)
	q, err := newDeviceQueue(ids)
	if err != nil {
		t.Fatal(err)
	}
	firstID := ids[0]
	ids[0] = "caller mutation"
	if q.members[0] != firstID {
		t.Fatal("queue borrowed mutable membership")
	}
	for want := range 2 {
		if index, ok := q.take(); !ok || index != want {
			t.Fatal("FIFO selection", index, ok)
		}
	}
	for _, id := range []string{q.members[2], uuid.NewString()} {
		if err := q.release(id); !errors.Is(err, xerror.ErrExecutionStaleState) || len(q.active) != 2 {
			t.Fatal("invalid release changed capacity", err)
		}
	}
	if index, ok := q.take(); !ok || index != 2 {
		t.Fatal("pending device was lost", index, ok)
	}
	if _, ok := q.take(); ok || q.next != len(q.members) || len(q.active) != len(q.members) {
		t.Fatal("selected membership was overbooked or throttled")
	}
	if err := q.release(firstID); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.take(); ok {
		t.Fatal("release redispatched an already selected member")
	}
	if err := q.release(firstID); !errors.Is(err, xerror.ErrExecutionStaleState) || len(q.active) != 2 {
		t.Fatal("double release changed capacity", err)
	}
	q.stop()
	q.stop()
	for _, id := range q.members[1:] {
		if err := q.release(id); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := q.take(); ok || len(q.active) != 0 {
		t.Fatal("stopped/exhausted queue dispatched work")
	}
}

func TestDeviceQueueBoundsAndValidation(t *testing.T) {
	for _, count := range []int{1, 64, 128} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			q, err := newDeviceQueue(queueMembers(count))
			if err != nil {
				t.Fatal(err)
			}
			for range count {
				if _, ok := q.take(); !ok {
					t.Fatal("capacity unavailable")
				}
			}
			position := q.next
			if _, ok := q.take(); ok || len(q.active) != count || q.next != position {
				t.Fatal("bound violated")
			}
		})
	}
	valid := uuid.NewString()
	for _, ids := range [][]string{nil, {valid, valid}, {valid, "invalid"}} {
		if q, err := newDeviceQueue(ids); q != nil || !errors.Is(err, xerror.ErrExecutionLifecycleInvalid) {
			t.Fatal("invalid membership", err)
		}
	}
}

func TestDeviceQueueConcurrentReservationsAndStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := New(nil)
		q, err := newDeviceQueue(queueMembers(32))
		if err != nil {
			t.Fatal(err)
		}
		start, finish := make(chan struct{}), make(chan struct{})
		reserved := make(chan int, 32)
		errorsFound := make(chan error, 32)
		var workers sync.WaitGroup
		for range 32 {
			workers.Go(func() {
				<-start
				l.mu.Lock()
				index, ok := q.take()
				l.mu.Unlock()
				if !ok {
					return
				}
				reserved <- index
				<-finish
				l.mu.Lock()
				err := q.release(q.members[index])
				l.mu.Unlock()
				if err != nil {
					errorsFound <- err
				}
			})
		}
		close(start)
		synctest.Wait()
		l.mu.Lock()
		active, position := len(q.active), q.next
		q.stop()
		l.mu.Unlock()
		close(finish)
		workers.Wait()
		if active != len(q.members) || position != len(q.members) {
			t.Fatal("concurrent limit violated", active, position)
		}
		close(reserved)
		close(errorsFound)
		seen := make(map[int]bool)
		for index := range reserved {
			if seen[index] {
				t.Fatal("duplicate dispatch", index)
			}
			seen[index] = true
		}
		for err := range errorsFound {
			t.Fatal(err)
		}
		if len(seen) != len(q.members) || len(q.active) != 0 {
			t.Fatal("lost reservations")
		}
		if _, ok := q.take(); ok || q.next != len(q.members) {
			t.Fatal("release reopened stopped queue")
		}
	})
}

func TestDeviceQueueTakeRacesWithStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := New(nil)
		q, err := newDeviceQueue(queueMembers(32))
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var contenders sync.WaitGroup
		var selectedAfterStop bool
		for range 32 {
			contenders.Go(func() {
				<-start
				l.mu.Lock()
				defer l.mu.Unlock()
				if _, ok := q.take(); ok && q.stopped {
					selectedAfterStop = true
				}
			})
		}
		contenders.Go(func() { <-start; l.mu.Lock(); q.stop(); l.mu.Unlock() })
		close(start)
		contenders.Wait()
		if selectedAfterStop || !q.stopped || len(q.active) > len(q.members) || q.next != len(q.active) {
			t.Fatal("stop/reservation fence failed")
		}
		for id := range q.active {
			if err := q.release(id); err != nil {
				t.Fatal(err)
			}
		}
		if _, ok := q.take(); ok {
			t.Fatal("capacity release bypassed the stop fence")
		}
	})
}

func TestGroupUsesSelectedCapacityAndStopsWithoutClosingLiveRAM(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, finish := make(chan struct{}), make(chan struct{})
		l, worker, request := managedWorkWithCheckpoints(t, groupCheckpoints{finish: func(ctx context.Context, req execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
			close(entered)
			<-finish
			return (checkpoints{}).FinishAttempt(ctx, req)
		}})
		if err := l.DispatchGroup(request.RunGroupID, worker, []runner.Request{request}); err != nil {
			t.Fatal(err)
		}
		<-entered
		l.mu.Lock()
		g := l.group
		members, count, position := len(g.queue.members), len(g.queue.active), g.queue.next
		l.mu.Unlock()
		if members != 1 || count != 1 || position != 1 {
			t.Fatal("capacity did not match selected membership")
		}
		store := g.store
		l.PrepareStop(constants.ExecutionStopOwnerLost, time.Now().Add(time.Second))
		l.mu.Lock()
		_, selected := g.queue.take()
		stopped, active := g.queue.stopped, len(g.queue.active)
		l.mu.Unlock()
		if selected || !stopped || active != 1 {
			t.Fatal("stop released active capacity prematurely")
		}
		if _, _, err := store.Get(t.Context(), request.DeviceExecutionID, constants.VariableScopeFlow, "missing"); err != nil {
			t.Fatal("stop closed live RAM", err)
		}
		close(finish)
		<-g.done
		if len(g.queue.active) != 0 {
			t.Fatal("worker did not release its slot")
		}
		if err := l.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}
