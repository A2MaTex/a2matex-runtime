package executionlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/end"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func awaitControlSnapshot(t *testing.T, l *Service, repo *repositories.ExecutionRepository, id, status string) execution.Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		snapshot, err := repo.SnapshotByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		l.mu.Lock()
		ready := l.failure == nil && l.group != nil && l.group.control == nil && l.group.paused == (status == constants.ExecutionPaused)
		failure := l.failure
		l.mu.Unlock()
		if failure != nil {
			t.Fatal(failure)
		}
		if snapshot.RunGroup.Status == status && ready {
			return snapshot
		}
		select {
		case <-ctx.Done():
			t.Fatal("control did not reach target", snapshot)
		case <-tick.C:
		}
	}
}

func TestGroupPauseResumePreservesOutputsRAMAndWorkerIdentity(t *testing.T) {
	entered := make(chan registry.Invocation, 4)
	release := []chan struct{}{make(chan struct{}), make(chan struct{})}
	defer releaseGroupBarriers(release)
	var mu sync.Mutex
	calls := make(map[string]int)
	var g *groupExecution
	handler := func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
		mu.Lock()
		calls[call.ADBSerial]++
		number := calls[call.ADBSerial]
		mu.Unlock()
		if number > 2 {
			return nil, errors.New("completed handler was repeated")
		}
		var duration int
		if err := json.Unmarshal(call.Inputs["durationMs"], &duration); err != nil {
			return nil, err
		}
		if number == 2 {
			value, present, err := g.store.Get(ctx, g.devices[0].DeviceExecutionID, constants.VariableScopeRunGroup, "value")
			if err != nil || !present || string(value) != "42" {
				return nil, errors.New("shared RAM lost on resume")
			}
			value, present, err = g.store.Get(ctx, g.devices[duration-1].DeviceExecutionID, constants.VariableScopeFlow, "value")
			if err != nil || !present || string(value) != strconv.Itoa(duration+9) {
				return nil, errors.New("isolated Flow RAM lost on resume")
			}
		}
		entered <- call
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release[number-1]:
		}
		return map[string]any{"durationMs": duration}, nil
	}
	l, _, devices, repo, _ := sqliteGroup(t, 2, handler)
	doc := &ir.Document{EntryNodeID: "delay", Nodes: []ir.Node{
		{ID: "delay", Type: "flow.delay", RuntimeHandler: "flow.delay", Config: ir.Object{},
			Inputs: ir.Object{"durationMs": json.RawMessage(`{"source":"WORKFLOW_INPUT","inputName":"duration"}`)}, TimeoutMS: 10_000, RetryPolicy: ir.RetryPolicy{MaxAttempts: 1}, ErrorPolicy: constants.ErrorPolicyStop},
		{ID: "check", Type: "flow.delay", RuntimeHandler: "flow.delay", Config: ir.Object{},
			Inputs: ir.Object{"durationMs": json.RawMessage(`{"source":"NODE_OUTPUT","nodeId":"delay","field":"durationMs"}`)}, TimeoutMS: 10_000, RetryPolicy: ir.RetryPolicy{MaxAttempts: 1}, ErrorPolicy: constants.ErrorPolicyStop},
		{ID: "end", Type: constants.EndNodeType, RuntimeHandler: constants.EndNodeType, Config: ir.Object{}, Inputs: ir.Object{}, TimeoutMS: 10_000, RetryPolicy: ir.RetryPolicy{MaxAttempts: 1}, ErrorPolicy: constants.ErrorPolicyStop},
	}, Edges: []ir.Edge{{ID: "a", SourceNodeID: "delay", SourcePort: "success", TargetNodeID: "check"}, {ID: "b", SourceNodeID: "check", SourcePort: "success", TargetNodeID: "end"}}}
	worker := lifecycleRunner(t, l.checkpoints, doc, []registry.Registration{
		{Type: "flow.delay", RuntimeHandler: "flow.delay", Run: handler},
		{Type: constants.EndNodeType, RuntimeHandler: constants.EndNodeType, Run: end.Invoke},
	})
	id := devices[0].RunGroupID
	if err := l.DispatchGroup(id, worker, devices); err != nil {
		t.Fatal(err)
	}
	for range devices {
		awaitGroupValue(t, entered)
	}
	l.mu.Lock()
	g = l.group
	l.mu.Unlock()
	writeGroup(t, g, 0, constants.VariableScopeRunGroup, "42")
	for i := range devices {
		writeGroup(t, g, i, constants.VariableScopeFlow, strconv.Itoa(i+10))
	}
	for cycle := range 2 {
		ctx, cancel := context.WithCancel(t.Context())
		if result, err := l.Pause(ctx, id); err != nil || result != constants.GroupControlAccepted {
			t.Fatal("pause request", result, err)
		}
		cancel() // Intent belongs to the owner after acceptance.
		if result, err := l.Pause(t.Context(), id); err != nil || result != constants.GroupControlAccepted {
			t.Fatal("pending duplicate", result, err)
		}
		if _, err := l.Resume(t.Context(), id); !errors.Is(err, xerror.ErrExecutionControlState) {
			t.Fatal("opposite pending action accepted", err)
		}
		close(release[cycle])
		paused := awaitControlSnapshot(t, l, repo, id, constants.ExecutionPaused)
		if paused.RunGroup.Summary.Paused != 2 || paused.RunGroup.FinishedAt != nil {
			t.Fatal("incomplete pause", paused)
		}
		for _, device := range devices {
			history, err := repo.Attempts(t.Context(), device.DeviceExecutionID, 0, 0)
			if err != nil || len(history.Attempts) != cycle+1 || history.Attempts[cycle].Status != constants.ExecutionSucceeded {
				t.Fatal("pause fabricated/lost an attempt", history, err)
			}
		}
		if result, err := l.Pause(t.Context(), id); err != nil || result != constants.GroupControlAlreadyTarget {
			t.Fatal("paused duplicate", result, err)
		}
		if result, err := l.Resume(t.Context(), id); err != nil || result != constants.GroupControlAccepted {
			t.Fatal("resume request", result, err)
		}
		if cycle == 0 {
			for range devices {
				call := awaitGroupValue(t, entered)
				var duration int
				if err := json.Unmarshal(call.Inputs["durationMs"], &duration); err != nil || call.ADBSerial != devices[duration-1].ADBSerial {
					t.Fatal("NODE_OUTPUT/input pairing lost", call, err)
				}
			}
			awaitControlSnapshot(t, l, repo, id, constants.ExecutionRunning)
		}
	}
	awaitGroupValue(t, g.done)
	final, err := repo.SnapshotByID(t.Context(), id)
	if err != nil || final.RunGroup.Summary.Succeeded != 2 {
		t.Fatal("continuation failed", final, err)
	}
	for _, device := range devices {
		history, _ := repo.Attempts(t.Context(), device.DeviceExecutionID, 0, 0)
		if len(history.Attempts) != 3 || history.Attempts[2].Sequence != 3 {
			t.Fatal("resume repeated/reset execution", history)
		}
	}
}

type resumeBlockRepository struct {
	*repositories.ExecutionRepository
	entered, release chan struct{}
}

func (r resumeBlockRepository) WithGroupControlTx(ctx context.Context, operation func(execution.GroupControlTx) error) error {
	return r.ExecutionRepository.WithGroupControlTx(ctx, func(tx execution.GroupControlTx) error {
		return operation(resumeBlockTx{GroupControlTx: tx, entered: r.entered, release: r.release})
	})
}

type resumeBlockTx struct {
	execution.GroupControlTx
	entered, release chan struct{}
}

func (tx resumeBlockTx) UpdateGroupControl(ctx context.Context, id string, pause bool, count int) error {
	if !pause {
		close(tx.entered)
		select {
		case <-tx.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return tx.GroupControlTx.UpdateGroupControl(ctx, id, pause, count)
}

func TestOwnerStopJoinsResumeCheckpointBeforeClosingStore(t *testing.T) {
	entered, release := make(chan registry.Invocation, 2), make(chan struct{})
	l, _, devices, repo, _ := sqliteGroup(t, 2, func(context.Context, registry.Invocation) (map[string]any, error) { return nil, nil })
	control := resumeBlockRepository{repo, make(chan struct{}), make(chan struct{}, 1)}
	defer close(control.release)
	cp := services.NewCheckpointService(control, nil)
	t.Cleanup(func() { cp.Shutdown(t.Context()) })
	l.checkpoints = cp
	doc := &ir.Document{EntryNodeID: "delay", Nodes: []ir.Node{
		{ID: "delay", Type: "flow.delay", RuntimeHandler: "flow.delay", Config: ir.Object{}, Inputs: ir.Object{"durationMs": json.RawMessage(`{"source":"LITERAL","value":0}`)},
			TimeoutMS: 10_000, RetryPolicy: ir.RetryPolicy{MaxAttempts: 1}, ErrorPolicy: constants.ErrorPolicyStop},
		{ID: "end", Type: constants.EndNodeType, RuntimeHandler: constants.EndNodeType, Config: ir.Object{}, Inputs: ir.Object{}, TimeoutMS: 1000, RetryPolicy: ir.RetryPolicy{MaxAttempts: 1}, ErrorPolicy: constants.ErrorPolicyStop},
	}, Edges: []ir.Edge{{ID: "next", SourceNodeID: "delay", SourcePort: "success", TargetNodeID: "end"}}}
	worker := lifecycleRunner(t, cp, doc, []registry.Registration{
		{Type: "flow.delay", RuntimeHandler: "flow.delay", Run: func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
			entered <- call
			select {
			case <-release:
				return map[string]any{"durationMs": 0}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}}, {Type: constants.EndNodeType, RuntimeHandler: constants.EndNodeType, Run: end.Invoke},
	})
	id := devices[0].RunGroupID
	if err := l.DispatchGroup(id, worker, devices); err != nil {
		t.Fatal(err)
	}
	for range devices {
		awaitGroupValue(t, entered)
	}
	l.Pause(t.Context(), id)
	close(release)
	awaitControlSnapshot(t, l, repo, id, constants.ExecutionPaused)
	l.Resume(t.Context(), id)
	awaitGroupValue(t, control.entered)
	l.PrepareStop(constants.ExecutionStopIntentional, time.Now().Add(3*time.Second))
	finished := make(chan error, 1)
	go func() { finished <- l.Shutdown(t.Context()) }()
	select {
	case err := <-finished:
		t.Fatal("shutdown did not join blocked control write", err)
	case <-time.After(10 * time.Millisecond):
	}
	// Let the write commit; the stop fence must still prevent worker wakeup.
	control.release <- struct{}{}
	if err := awaitGroupValue(t, finished); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.SnapshotByID(t.Context(), id)
	if err != nil || snapshot.RunGroup.Summary.Cancelled != 2 {
		t.Fatal("resume/stop race did not settle", snapshot, err)
	}
	for _, device := range devices {
		history, _ := repo.Attempts(t.Context(), device.DeviceExecutionID, 0, 0)
		if len(history.Attempts) != 1 {
			t.Fatal("resume dispatched after stop fence", history)
		}
	}
}

func TestPauseMixedDeviceFailureAndIntentionalStop(t *testing.T) {
	entered := make(chan registry.Invocation, 3)
	release := make(chan struct{})
	l, worker, devices, repo, _ := sqliteGroup(t, 3, func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
		entered <- call
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		var duration int
		json.Unmarshal(call.Inputs["durationMs"], &duration)
		if duration == 2 {
			return nil, xerror.CommandError{Code: constants.DeviceDisconnected, Message: "controlled device loss"}
		}
		return map[string]any{"durationMs": duration}, nil
	})
	id := devices[0].RunGroupID
	if err := l.DispatchGroup(id, worker, devices); err != nil {
		t.Fatal(err)
	}
	for range devices {
		awaitGroupValue(t, entered)
	}
	l.Pause(t.Context(), id)
	close(release)
	paused := awaitControlSnapshot(t, l, repo, id, constants.ExecutionPaused)
	if paused.RunGroup.Summary.Paused != 2 || paused.RunGroup.Summary.Failed != 1 {
		t.Fatal("terminal sibling was paused", paused)
	}
	l.PrepareStop(constants.ExecutionStopIntentional, time.Now().Add(3*time.Second))
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := l.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repo.SnapshotByID(t.Context(), id)
	if err != nil || snapshot.RunGroup.Status != constants.ExecutionCancelled || snapshot.RunGroup.Summary.Cancelled != 2 || snapshot.RunGroup.Summary.Failed != 1 {
		t.Fatal("paused stop lost settled outcomes", snapshot, err)
	}
}

func TestPauseFinishesRetryDelayAndPreservesPredecessor(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	calls := 0
	l, _, devices, repo, _ := sqliteGroup(t, 1, func(context.Context, registry.Invocation) (map[string]any, error) { return nil, nil })
	doc := &ir.Document{EntryNodeID: "delay", Nodes: []ir.Node{
		{ID: "delay", Type: "flow.delay", RuntimeHandler: "flow.delay", Config: ir.Object{}, Inputs: ir.Object{"durationMs": json.RawMessage(`{"source":"LITERAL","value":0}`)}, TimeoutMS: 10_000,
			RetryPolicy: ir.RetryPolicy{MaxAttempts: 2, DelayMS: 100}, ErrorPolicy: constants.ErrorPolicyStop},
		{ID: "end", Type: constants.EndNodeType, RuntimeHandler: constants.EndNodeType, Config: ir.Object{}, Inputs: ir.Object{}, TimeoutMS: 1000, RetryPolicy: ir.RetryPolicy{MaxAttempts: 1}, ErrorPolicy: constants.ErrorPolicyStop},
	}, Edges: []ir.Edge{{ID: "next", SourceNodeID: "delay", SourcePort: "success", TargetNodeID: "end"}}}
	worker := lifecycleRunner(t, l.checkpoints, doc, []registry.Registration{
		{Type: "flow.delay", RuntimeHandler: "flow.delay", Run: func(ctx context.Context, _ registry.Invocation) (map[string]any, error) {
			calls++
			if calls == 1 {
				entered <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return nil, xerror.CommandError{Code: constants.CommandStartFailed, Message: "controlled not-started command", Retryable: true}
			}
			return map[string]any{"durationMs": 0}, nil
		}},
		{Type: constants.EndNodeType, RuntimeHandler: constants.EndNodeType, Run: end.Invoke},
	})
	id := devices[0].RunGroupID
	if err := l.DispatchGroup(id, worker, devices); err != nil {
		t.Fatal(err)
	}
	awaitGroupValue(t, entered)
	l.Pause(t.Context(), id)
	started := time.Now()
	close(release)
	awaitControlSnapshot(t, l, repo, id, constants.ExecutionPaused)
	if time.Since(started) < 100*time.Millisecond {
		t.Fatal("pause cut short the retry delay")
	}
	l.mu.Lock()
	g := l.group
	l.mu.Unlock()
	l.Resume(t.Context(), id)
	awaitGroupValue(t, g.done)
	history, err := repo.Attempts(t.Context(), devices[0].DeviceExecutionID, 0, 0)
	if err != nil || len(history.Attempts) != 3 || history.Attempts[0].AttemptNumber != 1 || history.Attempts[1].AttemptNumber != 2 || history.Attempts[1].Sequence != 2 {
		t.Fatal("resume lost retry identity", history, err)
	}
}
