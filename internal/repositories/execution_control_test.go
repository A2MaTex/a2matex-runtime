package repositories

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func controlRequest(t *testing.T, repo *ExecutionRepository, id string) execution.GroupControlRequest {
	t.Helper()
	state, err := repo.ReadGroupControl(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	request := execution.GroupControlRequest{RunGroupID: id, Now: time.Now().UTC()}
	for _, member := range state.Members {
		if !member.Finished {
			request.Members = append(request.Members, member.Boundary)
		}
	}
	return request
}

func TestGroupControlAtomicCyclePreservesHistory(t *testing.T) {
	repo, cp, admission := checkpointFixture(t, 2)
	for i := range admission.Devices {
		begin := beginFixture(admission, i, 50+i)
		if _, err := cp.BeginAttempt(t.Context(), begin); err != nil {
			t.Fatal(err)
		}
		finish := finishFixture(begin)
		if i == 1 {
			finish.NextNodeID = "tap_next"
		}
		if _, err := cp.FinishAttempt(t.Context(), finish); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := repo.SnapshotByID(t.Context(), admission.RunGroupID)
	history, _ := repo.Attempts(t.Context(), admission.Devices[1].DeviceExecutionID, 0, 0)
	request := controlRequest(t, repo, admission.RunGroupID)
	for cycle := range 3 {
		state, _ := repo.ReadGroupControl(t.Context(), admission.RunGroupID)
		request = controlRequest(t, repo, admission.RunGroupID)
		paused, err := cp.PauseGroup(t.Context(), request)
		if err != nil || paused.Disposition != constants.GroupControlCommitted || paused.Changed != 1 {
			t.Fatal("pause", cycle, paused, err)
		}
		after, _ := repo.SnapshotByID(t.Context(), admission.RunGroupID)
		cursor, _ := execution.ParseEventCursor(paused.Cursor)
		if after.RunGroup.Status != constants.ExecutionPaused || after.RunGroup.Summary.Paused != 1 || cursor != state.Watermark+2 ||
			!reflect.DeepEqual(after.DeviceExecutions[0], before.DeviceExecutions[0]) {
			t.Fatal("partial or destructive pause", after)
		}
		if duplicate, err := cp.PauseGroup(t.Context(), request); err != nil || duplicate.Disposition != constants.GroupControlAlreadyTarget {
			t.Fatal("duplicate pause", duplicate, err)
		}
		resumed, err := cp.ResumeGroup(t.Context(), controlRequest(t, repo, admission.RunGroupID))
		if err != nil || resumed.Changed != 1 || resumed.Disposition != constants.GroupControlCommitted {
			t.Fatal("resume", resumed, err)
		}
		if duplicate, err := cp.ResumeGroup(t.Context(), request); err != nil || duplicate.Disposition != constants.GroupControlAlreadyTarget {
			t.Fatal("duplicate resume", duplicate, err)
		}
		after, _ = repo.SnapshotByID(t.Context(), admission.RunGroupID)
		if after.RunGroup.Summary.Queued != 1 || after.DeviceExecutions[1].NextNodeID == nil || *after.DeviceExecutions[1].NextNodeID != "tap_next" ||
			!reflect.DeepEqual(after.DeviceExecutions[1].StartedAt, before.DeviceExecutions[1].StartedAt) {
			t.Fatal("resume lost continuation", after)
		}
	}
	afterHistory, _ := repo.Attempts(t.Context(), admission.Devices[1].DeviceExecutionID, 0, 0)
	if !reflect.DeepEqual(history, afterHistory) {
		t.Fatal("pause/resume changed attempt history")
	}
}

func TestGroupControlRejectsIncompleteOrActiveEvidence(t *testing.T) {
	for _, mode := range []string{"missing", "duplicate", "sequence", "next node", "active"} {
		t.Run(mode, func(t *testing.T) {
			repo, cp, admission := checkpointFixture(t, 2)
			if mode == "active" {
				if _, err := cp.BeginAttempt(t.Context(), beginFixture(admission, 1, 50)); err != nil {
					t.Fatal(err)
				}
			}
			request := controlRequest(t, repo, admission.RunGroupID)
			switch mode {
			case "missing":
				request.Members = request.Members[:1]
			case "duplicate":
				request.Members[1] = request.Members[0]
			case "sequence":
				request.Members[1].Sequence = 1
				request.Members[1].AttemptID = testExecutionID(90)
			case "next node":
				request.Members[1].NextNodeID = "wrong_node"
			}
			before, _ := repo.SnapshotByID(t.Context(), admission.RunGroupID)
			result, err := cp.PauseGroup(t.Context(), request)
			after, _ := repo.SnapshotByID(t.Context(), admission.RunGroupID)
			if err != nil || result.Disposition != constants.GroupControlStale || !reflect.DeepEqual(before, after) {
				t.Fatal("invalid control mutated records", result, err)
			}
		})
	}
}

type controlFaultRepository struct {
	*ExecutionRepository
	unknown, rollback, mismatch bool
}

func (r controlFaultRepository) WithGroupControlTx(ctx context.Context, operation func(execution.GroupControlTx) error) error {
	err := r.ExecutionRepository.WithGroupControlTx(ctx, func(tx execution.GroupControlTx) error {
		if err := operation(tx); err != nil {
			return err
		}
		if r.rollback {
			return xerror.ErrExecutionPersistence
		}
		return nil
	})
	if err == nil && r.unknown {
		return xerror.ErrExecutionCommitUnknown
	}
	return err
}

func (r controlFaultRepository) ReadGroupControl(ctx context.Context, id string) (execution.GroupControlState, error) {
	state, err := r.ExecutionRepository.ReadGroupControl(ctx, id)
	if r.mismatch {
		state.Status = constants.ExecutionRunning
	}
	return state, err
}

func TestGroupControlLargeAtomicBatchAndCommitReadBack(t *testing.T) {
	for _, mode := range []string{"committed", "rollback", "unknown confirmed", "unknown mismatch"} {
		t.Run(mode, func(t *testing.T) {
			repo := executionStore(t)
			admission := testAdmission(0)
			admission.Devices = nil
			for i := range 128 {
				admission.Devices = append(admission.Devices, execution.AdmissionDevice{DeviceExecutionID: testExecutionID(10 + i), ADBSerial: testExecutionID(10 + i), EntryNodeID: "start"})
			}
			if _, err := repo.Admit(t.Context(), admission); err != nil {
				t.Fatal(err)
			}
			cp := services.NewCheckpointService(controlFaultRepository{repo, mode != "committed" && mode != "rollback", mode == "rollback", mode == "unknown mismatch"}, nil)
			defer cp.Shutdown(t.Context())
			before, _ := repo.SnapshotByID(t.Context(), admission.RunGroupID)
			result, err := cp.PauseGroup(t.Context(), controlRequest(t, repo, admission.RunGroupID))
			after, _ := repo.SnapshotByID(t.Context(), admission.RunGroupID)
			if mode == "rollback" {
				if !errors.Is(err, xerror.ErrCheckpointFatal) || !reflect.DeepEqual(before, after) {
					t.Fatal("control rollback leaked writes", result, err)
				}
				return
			}
			if mode == "unknown mismatch" {
				if !errors.Is(err, xerror.ErrCheckpointFatal) {
					t.Fatal("unconfirmed control was acknowledged", err)
				}
				return
			}
			if err != nil || result.Changed != 128 || after.RunGroup.Summary.Paused != 128 || after.Cursor != "258" {
				t.Fatal("large checkpoint or read-back lost members/events", result, after, err)
			}
		})
	}
}
