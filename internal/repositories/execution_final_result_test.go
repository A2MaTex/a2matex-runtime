package repositories

import (
	"reflect"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

// These are persistence fixtures, not Phase H multi-device scheduling.
func TestG21GroupFinalResultAggregation(t *testing.T) {
	for _, tc := range []struct {
		name, first, second, group string
		partial, final             execution.RunSummary
	}{
		{"all succeed", "SUCCEEDED", "SUCCEEDED", "SUCCEEDED", execution.RunSummary{Total: 2, Succeeded: 1, Queued: 1}, execution.RunSummary{Total: 2, Succeeded: 2}},
		{"failure finishes first", "FAILED", "SUCCEEDED", "FAILED", execution.RunSummary{Total: 2, Failed: 1, Queued: 1}, execution.RunSummary{Total: 2, Failed: 1, Succeeded: 1}},
		{"failure finishes last", "SUCCEEDED", "FAILED", "FAILED", execution.RunSummary{Total: 2, Succeeded: 1, Queued: 1}, execution.RunSummary{Total: 2, Failed: 1, Succeeded: 1}},
		{"both fail", "FAILED", "FAILED", "FAILED", execution.RunSummary{Total: 2, Failed: 1, Queued: 1}, execution.RunSummary{Total: 2, Failed: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, service, admission := checkpointFixture(t, 2)
			var previous execution.DeviceExecution
			var finish execution.FinishAttemptRequest
			for i, status := range []string{tc.first, tc.second} {
				begin := beginFixture(admission, i, 50+i)
				begin.Now = begin.Now.Add(time.Duration(i) * 3 * time.Second)
				if result, err := service.BeginAttempt(t.Context(), begin); err != nil || result.Disposition != constants.CheckpointCommitted {
					t.Fatalf("begin: %+v %v", result, err)
				}
				finish = finishFixture(begin)
				finish.Status = status
				if status == constants.ExecutionFailed {
					finish.Output, finish.Error = nil, []byte(failedCheckpointError)
				}
				if result, err := service.FinishAttempt(t.Context(), finish); err != nil || result.Disposition != constants.CheckpointCommitted {
					t.Fatalf("finish: %+v %v", result, err)
				}
				snapshot, err := repo.SnapshotByID(t.Context(), admission.RunGroupID)
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					if snapshot.RunGroup.Status != constants.ExecutionRunning || snapshot.RunGroup.FinishedAt != nil || snapshot.RunGroup.Summary != tc.partial {
						t.Fatalf("premature group completion: %+v", snapshot)
					}
					previous = snapshot.DeviceExecutions[0]
					continue
				}
				if snapshot.RunGroup.Status != tc.group || snapshot.RunGroup.Summary != tc.final || snapshot.RunGroup.FinishedAt == nil || snapshot.DeviceExecutions[1].FinishedAt == nil ||
					!reflect.DeepEqual(previous, snapshot.DeviceExecutions[0]) || *snapshot.RunGroup.FinishedAt != *snapshot.DeviceExecutions[1].FinishedAt {
					t.Fatalf("final aggregation: %+v", snapshot)
				}
			}
			before, err := repo.SnapshotByID(t.Context(), admission.RunGroupID)
			if err != nil {
				t.Fatal(err)
			}
			// A late, different completion cannot rewrite an already-final attempt.
			finish.Status, finish.Output, finish.Error = constants.ExecutionSucceeded, []byte(`{}`), nil
			if result, err := service.FinishAttempt(t.Context(), finish); err != nil || result.Disposition != constants.CheckpointAlreadyFinalized {
				t.Fatalf("duplicate completion: %+v %v", result, err)
			}
			after, err := repo.SnapshotByID(t.Context(), admission.RunGroupID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("late completion changed terminal state/cursor", err)
			}
		})
	}
}

func TestG21GroupStopPreservesTerminalSibling(t *testing.T) {
	for _, status := range []string{constants.ExecutionSucceeded, constants.ExecutionFailed} {
		for _, cause := range []string{"intentional", "startup recovery"} {
			t.Run(status+"/"+cause, func(t *testing.T) {
				repo, service, admission := checkpointFixture(t, 2)
				begin := beginFixture(admission, 0, 50)
				requireBegin(t, service, begin, 1, "5")
				finish := finishFixture(begin)
				finish.Status = status
				want := execution.RunSummary{Total: 2, Succeeded: 1}
				if status == constants.ExecutionFailed {
					finish.Output, finish.Error = nil, []byte(failedCheckpointError)
					want.Succeeded, want.Failed = 0, 1
				}
				requireFinish(t, service, finish, 1, "7")
				before, err := repo.SnapshotByID(t.Context(), admission.RunGroupID)
				if err != nil {
					t.Fatal(err)
				}
				group := constants.ExecutionCancelled
				if cause == "intentional" {
					cancelRemaining(t, service)
					want.Cancelled = 1
				} else {
					if err := recoverStartup(t, repo); err != nil {
						t.Fatal(err)
					}
					group, want.Interrupted = constants.ExecutionInterrupted, 1
				}
				after, err := repo.SnapshotByID(t.Context(), admission.RunGroupID)
				if err != nil || after.RunGroup.Status != group || after.RunGroup.Summary != want || after.RunGroup.FinishedAt == nil ||
					!reflect.DeepEqual(before.DeviceExecutions[0], after.DeviceExecutions[0]) || after.DeviceExecutions[1].StartedAt != nil || after.DeviceExecutions[1].NextNodeID != nil {
					t.Fatalf("stop changed terminal sibling or miscounted results: %+v %v", after, err)
				}
			})
		}
	}
}
