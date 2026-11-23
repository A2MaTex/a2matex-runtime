package executionlifecycle

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type resumeFailureRepository struct {
	*repositories.ExecutionRepository
}

func (r resumeFailureRepository) WithGroupControlTx(ctx context.Context, operation func(execution.GroupControlTx) error) error {
	return r.ExecutionRepository.WithGroupControlTx(ctx, func(tx execution.GroupControlTx) error {
		return operation(resumeFailureTx{tx})
	})
}

type resumeFailureTx struct{ execution.GroupControlTx }

func (tx resumeFailureTx) UpdateGroupControl(ctx context.Context, id string, pause bool, count int) error {
	if err := tx.GroupControlTx.UpdateGroupControl(ctx, id, pause, count); err != nil {
		return err
	}
	if !pause {
		return xerror.ErrExecutionPersistence // Force rollback after both state updates.
	}
	return nil
}

func TestResumeFailureNeverReleasesParkedWorkers(t *testing.T) {
	entered, release := make(chan registry.Invocation, 2), make(chan struct{})
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
		return resumeFailureRepository{repo}
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
	if _, err := l.Resume(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	awaitGroupValue(t, g.done)
	snapshot, err := repo.SnapshotByID(t.Context(), id)
	if err != nil || snapshot.RunGroup.Status != constants.ExecutionPaused || snapshot.RunGroup.Summary.Paused != 2 {
		t.Fatal("failed Resume leaked a state change", snapshot, err)
	}
	for _, device := range devices {
		history, err := repo.Attempts(t.Context(), device.DeviceExecutionID, 0, 0)
		if err != nil || len(history.Attempts) != 1 {
			t.Fatal("handler dispatched after unconfirmed Resume", history, err)
		}
	}
}

func TestAllTerminalOutcomesBeatPendingPause(t *testing.T) {
	entered, release := make(chan registry.Invocation, 2), make(chan struct{})
	l, worker, devices, repo, _ := sqliteGroup(t, 2, func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
		entered <- call
		select {
		case <-release:
			return nil, xerror.CommandError{Code: constants.DeviceDisconnected, Message: "controlled device loss"}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	id := devices[0].RunGroupID
	if err := l.DispatchGroup(id, worker, devices); err != nil {
		t.Fatal(err)
	}
	for range devices {
		awaitGroupValue(t, entered)
	}
	l.mu.Lock()
	g := l.group
	l.mu.Unlock()
	if _, err := l.Pause(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	close(release)
	awaitGroupValue(t, g.done)
	snapshot, err := repo.SnapshotByID(t.Context(), id)
	if err != nil || snapshot.RunGroup.Status != constants.ExecutionFailed || snapshot.RunGroup.Summary.Failed != 2 {
		t.Fatal("Pause replaced a terminal result", snapshot, err)
	}
}
