package executionlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/attempts"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/end"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

type cancellationGateRepository struct {
	*repositories.ExecutionRepository
	entered, release chan struct{}
	failure          error
}

func (r cancellationGateRepository) WithCancellationTx(ctx context.Context, operation func(execution.CancellationTx) error) error {
	if r.entered != nil {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return r.ExecutionRepository.WithCancellationTx(ctx, func(tx execution.CancellationTx) error {
		if err := operation(tx); err != nil {
			return err
		}
		return r.failure // A failure here rolls back actual writes/events.
	})
}

func TestCancelGroupPreservesHistoryAndStartsFreshRun(t *testing.T) {
	for _, position := range []string{"active", "paused", "pause pending"} {
		t.Run(position, func(t *testing.T) {
			entered, release := make(chan registry.Invocation, 2), make(chan struct{})
			defer releaseGroupBarriers([]chan struct{}{release})
			var fresh atomic.Bool
			gate := cancellationGateRepository{entered: make(chan struct{}), release: make(chan struct{})}
			defer close(gate.release)
			l, worker, devices, repo, terminal := sqliteGroup(t, 2, func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
				if !fresh.Load() {
					entered <- call
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-release:
					}
				}
				var duration int
				if err := json.Unmarshal(call.Inputs["durationMs"], &duration); err != nil {
					return nil, err
				}
				return map[string]any{"durationMs": duration}, nil
			}, groupTestOptions{wrap: func(repo *repositories.ExecutionRepository) execution.CheckpointRepository {
				gate.ExecutionRepository = repo
				return gate
			}})
			id := devices[0].RunGroupID
			if err := l.DispatchGroup(id, worker, devices); err != nil {
				t.Fatal(err)
			}
			for range devices {
				awaitGroupValue(t, entered)
			}
			l.mu.Lock()
			g := l.group
			oldStore := g.store
			l.mu.Unlock()
			if _, err := l.Cancel(t.Context(), uuid.NewString()); !errors.Is(err, xerror.ErrExecutionControlState) {
				t.Fatal("wrong group", err)
			}
			cancelled, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := l.Cancel(cancelled, id); !errors.Is(err, context.Canceled) {
				t.Fatal("cancelled submission", err)
			}
			if position != "active" {
				if _, err := l.Pause(t.Context(), id); err != nil {
					t.Fatal(err)
				}
				if position == "paused" {
					close(release)
					awaitControlSnapshot(t, l, repo, id, constants.ExecutionPaused)
				}
			}
			submit, disconnect := context.WithCancel(t.Context())
			if result, err := l.Cancel(submit, id); err != nil || result != constants.GroupControlAccepted {
				t.Fatal(result, err)
			}
			disconnect()
			awaitGroupValue(t, gate.entered) // Workers are joined; keep settlement/retirement pending.
			if result, err := l.Cancel(t.Context(), id); err != nil || result != constants.GroupControlAccepted {
				t.Fatal("duplicate pending", result, err)
			}
			if _, err := l.Resume(t.Context(), id); !errors.Is(err, xerror.ErrExecutionControlState) {
				t.Fatal("resume during Cancel", err)
			}
			if _, err := l.Pause(t.Context(), id); !errors.Is(err, xerror.ErrExecutionControlState) {
				t.Fatal("pause during Cancel", err)
			}
			select {
			case <-g.done:
				t.Fatal("retired before settlement")
			default:
			}
			gate.release <- struct{}{}
			awaitGroupValue(t, g.done)
			snapshot, err := repo.SnapshotByID(t.Context(), id)
			if err != nil || snapshot.RunGroup.Status != constants.ExecutionCancelled || snapshot.RunGroup.Summary.Cancelled != 2 {
				t.Fatal(snapshot, err)
			}
			for _, device := range devices {
				page, err := repo.Attempts(t.Context(), device.DeviceExecutionID, 0, 0)
				want := constants.ExecutionCancelled
				if position == "paused" {
					want = constants.ExecutionSucceeded
				}
				if err != nil || len(page.Attempts) != 1 || page.Attempts[0].Status != want {
					t.Fatal("lost/repeated history", page, err)
				}
			}
			if _, _, err := oldStore.Get(t.Context(), devices[0].DeviceExecutionID, constants.VariableScopeFlow, "old"); err == nil {
				t.Fatal("old RAM still live")
			}
			// Consume terminal-attempt notifications before reusing the same worker fixture.
			if position != "paused" {
				for range devices {
					awaitGroupValue(t, terminal)
				}
			}
			fresh.Store(true)
			newID, next := groupDevices(2)
			admission := execution.Admission{RequestID: uuid.NewString(), RunGroupID: newID, Now: time.Now().UTC()}
			for i := range next {
				next[i].WorkflowInputs = devices[i].WorkflowInputs
				admission.Devices = append(admission.Devices, execution.AdmissionDevice{DeviceExecutionID: next[i].DeviceExecutionID, ADBSerial: next[i].ADBSerial, EntryNodeID: "delay"})
			}
			if _, err := repo.Admit(t.Context(), admission); err != nil {
				t.Fatal(err)
			}
			if err := l.DispatchGroup(newID, worker, next); err != nil {
				t.Fatal(err)
			}
			for range next {
				awaitGroupValue(t, terminal)
			}
			result, err := repo.SnapshotByID(t.Context(), newID)
			if err != nil || result.RunGroup.Status != constants.ExecutionSucceeded {
				t.Fatal("next run inherited cancellation", result, err)
			}
		})
	}
}

func TestCancelGroupJoinsPendingResumeAndOwnerStop(t *testing.T) {
	entered, release := make(chan registry.Invocation, 2), make(chan struct{})
	defer releaseGroupBarriers([]chan struct{}{release})
	var block resumeBlockRepository
	l, worker, devices, repo, _ := sqliteGroup(t, 2, func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
		entered <- call
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		var duration int
		if err := json.Unmarshal(call.Inputs["durationMs"], &duration); err != nil {
			return nil, err
		}
		return map[string]any{"durationMs": duration}, nil
	}, groupTestOptions{wrap: func(repo *repositories.ExecutionRepository) execution.CheckpointRepository {
		block = resumeBlockRepository{repo, make(chan struct{}), make(chan struct{}, 1)}
		return block
	}})
	defer close(block.release)
	id := devices[0].RunGroupID
	if err := l.DispatchGroup(id, worker, devices); err != nil {
		t.Fatal(err)
	}
	for range devices {
		awaitGroupValue(t, entered)
	}
	if _, err := l.Pause(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	close(release)
	awaitControlSnapshot(t, l, repo, id, constants.ExecutionPaused)
	if _, err := l.Resume(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	awaitGroupValue(t, block.entered)
	if _, err := l.Cancel(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	g := l.group
	original := g.cancellation.deadline
	l.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	l.PrepareStop(constants.ExecutionStopIntentional, deadline)
	l.mu.Lock()
	shortened := g.cancellation.deadline
	l.mu.Unlock()
	if !shortened.Before(original) || !shortened.Equal(deadline) {
		t.Fatal("Cancel budget not shortened", shortened, original)
	}
	finished := make(chan error, 1)
	go func() { finished <- l.Shutdown(t.Context()) }()
	select {
	case <-g.done:
		t.Fatal("control write not joined")
	default:
	}
	block.release <- struct{}{}
	if err := awaitGroupValue(t, finished); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.SnapshotByID(t.Context(), id)
	if err != nil || snapshot.RunGroup.Summary.Cancelled != 2 {
		t.Fatal(snapshot, err)
	}
	for _, device := range devices {
		page, _ := repo.Attempts(t.Context(), device.DeviceExecutionID, 0, 0)
		if len(page.Attempts) != 1 {
			t.Fatal("Resume released after Cancel", page)
		}
	}
}

func TestCancelGroupResidualFailureFencesRuntime(t *testing.T) {
	entered, release := make(chan registry.Invocation, 2), make(chan struct{})
	defer releaseGroupBarriers([]chan struct{}{release})
	l, worker, devices, repo, _ := sqliteGroup(t, 2, func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
		entered <- call
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		var duration int
		if err := json.Unmarshal(call.Inputs["durationMs"], &duration); err != nil {
			return nil, err
		}
		return map[string]any{"durationMs": duration}, nil
	}, groupTestOptions{wrap: func(repo *repositories.ExecutionRepository) execution.CheckpointRepository {
		return cancellationGateRepository{ExecutionRepository: repo, failure: xerror.ErrExecutionPersistence}
	}, expectedStop: xerror.ErrCheckpointFatal})
	id := devices[0].RunGroupID
	if err := l.DispatchGroup(id, worker, devices); err != nil {
		t.Fatal(err)
	}
	for range devices {
		awaitGroupValue(t, entered)
	}
	if _, err := l.Pause(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	close(release)
	awaitControlSnapshot(t, l, repo, id, constants.ExecutionPaused)
	l.mu.Lock()
	g := l.group
	l.mu.Unlock()
	if _, err := l.Cancel(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	awaitGroupValue(t, g.done)
	snapshot, err := repo.SnapshotByID(t.Context(), id)
	if err != nil || snapshot.RunGroup.Status != constants.ExecutionPaused {
		t.Fatal("rollback leaked cancellation", snapshot, err)
	}
	if !errors.Is(l.Wait(), xerror.ErrExecutionPersistence) {
		t.Fatal("failure was hidden")
	}
	if _, err := l.BeginAdmission(t.Context()); !errors.Is(err, xerror.ErrExecutionStopping) {
		t.Fatal("failure gate reopened", err)
	}
}

type cancelBeforeBeginRepository struct {
	*repositories.ExecutionRepository
	entered chan struct{}
}

func (r cancelBeforeBeginRepository) WithCheckpointTx(ctx context.Context, operation func(execution.CheckpointTx) error) error {
	r.entered <- struct{}{}
	<-ctx.Done()
	return ctx.Err()
}

func TestCancelGroupBeforeBeginCreatesNoAttempt(t *testing.T) {
	entered := make(chan struct{}, 2)
	l, worker, devices, repo, _ := sqliteGroup(t, 2, func(context.Context, registry.Invocation) (map[string]any, error) {
		t.Error("cancelled queued member invoked handler")
		return nil, nil
	}, groupTestOptions{wrap: func(repo *repositories.ExecutionRepository) execution.CheckpointRepository {
		return cancelBeforeBeginRepository{repo, entered}
	}})
	id := devices[0].RunGroupID
	if err := l.DispatchGroup(id, worker, devices); err != nil {
		t.Fatal(err)
	}
	awaitGroupValue(t, entered)
	l.mu.Lock()
	g := l.group
	l.mu.Unlock()
	if _, err := l.Cancel(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	awaitGroupValue(t, g.done)
	snapshot, err := repo.SnapshotByID(t.Context(), id)
	if err != nil || snapshot.RunGroup.Summary.Cancelled != 2 {
		t.Fatal(snapshot, err)
	}
	for _, device := range snapshot.DeviceExecutions {
		page, err := repo.Attempts(t.Context(), device.ID, 0, 0)
		if err != nil || len(page.Attempts) != 0 || device.StartedAt != nil {
			t.Fatal("fabricated begun attempt", device, page, err)
		}
	}
}

func TestCancelGroupInterruptsRetryGap(t *testing.T) {
	l, _, devices, repo, _ := sqliteGroup(t, 1, func(context.Context, registry.Invocation) (map[string]any, error) { return nil, nil })
	var calls atomic.Int32
	doc := &ir.Document{EntryNodeID: "delay", Nodes: []ir.Node{
		{ID: "delay", Type: "flow.delay", RuntimeHandler: "flow.delay", Config: ir.Object{}, Inputs: ir.Object{"durationMs": json.RawMessage(`{"source":"LITERAL","value":0}`)}, TimeoutMS: 10_000,
			RetryPolicy: ir.RetryPolicy{MaxAttempts: 2, DelayMS: 60_000}, ErrorPolicy: constants.ErrorPolicyStop},
		{ID: "end", Type: constants.EndNodeType, RuntimeHandler: constants.EndNodeType, Config: ir.Object{}, Inputs: ir.Object{}, TimeoutMS: 1000, RetryPolicy: ir.RetryPolicy{MaxAttempts: 1}, ErrorPolicy: constants.ErrorPolicyStop},
	}, Edges: []ir.Edge{{ID: "next", SourceNodeID: "delay", SourcePort: "success", TargetNodeID: "end"}}}
	worker := lifecycleRunner(t, l.checkpoints, doc, []registry.Registration{
		{Type: "flow.delay", RuntimeHandler: "flow.delay", Run: func(context.Context, registry.Invocation) (map[string]any, error) {
			calls.Add(1)
			return nil, xerror.CommandError{Code: constants.CommandStartFailed, Message: "controlled not started", Retryable: true}
		}}, {Type: constants.EndNodeType, RuntimeHandler: constants.EndNodeType, Run: end.Invoke},
	})
	id := devices[0].RunGroupID
	if err := l.DispatchGroup(id, worker, devices); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		page, err := repo.Attempts(ctx, devices[0].DeviceExecutionID, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Attempts) == 1 && page.Attempts[0].Status == constants.ExecutionFailed {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("retry checkpoint missing")
		case <-tick.C:
		}
	}
	l.mu.Lock()
	g := l.group
	l.mu.Unlock()
	if _, err := l.Cancel(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	awaitGroupValue(t, g.done)
	page, err := repo.Attempts(t.Context(), devices[0].DeviceExecutionID, 0, 0)
	if err != nil || len(page.Attempts) != 1 || page.Attempts[0].Status != constants.ExecutionFailed || calls.Load() != 1 {
		t.Fatal("retried/rewrote failed attempt", page, err)
	}
	snapshot, _ := repo.SnapshotByID(t.Context(), id)
	if snapshot.RunGroup.Status != constants.ExecutionCancelled {
		t.Fatal(snapshot)
	}
}

func TestCancelGroupJoinDeadlineDoesNotCloseLiveRAM(t *testing.T) {
	entered, release := make(chan registry.Invocation, 1), make(chan struct{})
	defer releaseGroupBarriers([]chan struct{}{release})
	l, worker, devices, _, _ := sqliteGroup(t, 1, func(_ context.Context, call registry.Invocation) (map[string]any, error) {
		entered <- call
		<-release // Deliberately uncooperative; teardown always releases it.
		return map[string]any{"durationMs": 1}, nil
	}, groupTestOptions{expectedStop: context.DeadlineExceeded})
	id := devices[0].RunGroupID
	if err := l.DispatchGroup(id, worker, devices); err != nil {
		t.Fatal(err)
	}
	awaitGroupValue(t, entered)
	if _, err := l.Cancel(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	g := l.group
	store := g.store
	g.cancellation.shorten(time.Now().Add(20 * time.Millisecond))
	l.mu.Unlock()
	if err := l.Wait(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("deadline not surfaced", err)
	}
	if _, _, err := store.Get(t.Context(), devices[0].DeviceExecutionID, constants.VariableScopeFlow, "value"); err != nil {
		t.Fatal("closed RAM used by live handler", err)
	}
	select {
	case <-g.done:
		t.Fatal("unjoined group retired")
	default:
	}
	close(release)
	awaitGroupValue(t, g.done)
}

func TestCancelGroupPreservesFailedSibling(t *testing.T) {
	entered := make(chan registry.Invocation, 2)
	l, worker, devices, repo, terminal := sqliteGroup(t, 2, func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
		entered <- call
		if string(call.Inputs["durationMs"]) == "1" {
			return nil, xerror.CommandError{Code: constants.DeviceDisconnected, Message: "controlled failure"}
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	id := devices[0].RunGroupID
	if err := l.DispatchGroup(id, worker, devices); err != nil {
		t.Fatal(err)
	}
	for range devices {
		awaitGroupValue(t, entered)
	}
	awaitGroupValue(t, terminal) // The first member's failure is durable.
	before, _ := repo.Attempts(t.Context(), devices[0].DeviceExecutionID, 0, 0)
	l.mu.Lock()
	g := l.group
	l.mu.Unlock()
	if _, err := l.Cancel(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	awaitGroupValue(t, g.done)
	snapshot, err := repo.SnapshotByID(t.Context(), id)
	if err != nil || snapshot.RunGroup.Status != constants.ExecutionCancelled || snapshot.RunGroup.Summary.Failed != 1 || snapshot.RunGroup.Summary.Cancelled != 1 {
		t.Fatal(snapshot, err)
	}
	after, _ := repo.Attempts(t.Context(), devices[0].DeviceExecutionID, 0, 0)
	if string(before.Attempts[0].Error) != string(after.Attempts[0].Error) || after.Attempts[0].Status != constants.ExecutionFailed {
		t.Fatal("terminal sibling changed", after)
	}
}

type cancelAfterCommitGate struct {
	attempts.Checkpoints
	entered, release chan struct{}
}

func (g cancelAfterCommitGate) FinishAttempt(ctx context.Context, req execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
	result, err := g.Checkpoints.FinishAttempt(ctx, req)
	if err == nil && req.NextNodeID == "" {
		close(g.entered)
		<-g.release // A confirmed terminal outcome must survive a later Cancel.
	}
	return result, err
}

func TestCancelGroupPreservesNaturalTerminalWinner(t *testing.T) {
	l, _, devices, repo, _ := sqliteGroup(t, 1, func(context.Context, registry.Invocation) (map[string]any, error) { return nil, nil })
	gate := cancelAfterCommitGate{l.checkpoints, make(chan struct{}), make(chan struct{})}
	defer releaseGroupBarriers([]chan struct{}{gate.release})
	doc := &ir.Document{EntryNodeID: "delay", Nodes: []ir.Node{
		{ID: "delay", Type: "flow.delay", RuntimeHandler: "flow.delay", Config: ir.Object{}, Inputs: ir.Object{"durationMs": json.RawMessage(`{"source":"LITERAL","value":0}`)}, TimeoutMS: 10_000, RetryPolicy: ir.RetryPolicy{MaxAttempts: 1}, ErrorPolicy: constants.ErrorPolicyStop},
		{ID: "end", Type: constants.EndNodeType, RuntimeHandler: constants.EndNodeType, Config: ir.Object{}, Inputs: ir.Object{}, TimeoutMS: 1000, RetryPolicy: ir.RetryPolicy{MaxAttempts: 1}, ErrorPolicy: constants.ErrorPolicyStop},
	}, Edges: []ir.Edge{{ID: "next", SourceNodeID: "delay", SourcePort: "success", TargetNodeID: "end"}}}
	worker := lifecycleRunner(t, gate, doc, []registry.Registration{
		{Type: "flow.delay", RuntimeHandler: "flow.delay", Run: func(context.Context, registry.Invocation) (map[string]any, error) {
			return map[string]any{"durationMs": 0}, nil
		}},
		{Type: constants.EndNodeType, RuntimeHandler: constants.EndNodeType, Run: end.Invoke},
	})
	id := devices[0].RunGroupID
	if err := l.DispatchGroup(id, worker, devices); err != nil {
		t.Fatal(err)
	}
	awaitGroupValue(t, gate.entered)
	before, _ := repo.SnapshotByID(t.Context(), id)
	l.mu.Lock()
	g := l.group
	l.mu.Unlock()
	if _, err := l.Cancel(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	awaitGroupValue(t, g.done)
	after, err := repo.SnapshotByID(t.Context(), id)
	if err != nil || after.RunGroup.Status != constants.ExecutionSucceeded || after.Cursor != before.Cursor {
		t.Fatal("Cancel rewrote natural winner", after, err)
	}
}
