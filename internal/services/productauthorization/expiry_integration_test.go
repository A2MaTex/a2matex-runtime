package productauthorization

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/variables"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productworkflow"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Reuse the actual checkpoint transaction/reconciliation path; these wrappers
// simulate only the driver's ambiguous acknowledgement, not database state.
type ambiguousRepository struct {
	*repositories.ExecutionRepository
	rollback bool
}

func (r ambiguousRepository) WithGroupControlTx(ctx context.Context, operation func(execution.GroupControlTx) error) error {
	err := r.ExecutionRepository.WithGroupControlTx(ctx, func(tx execution.GroupControlTx) error {
		if err := operation(tx); err != nil {
			return err
		}
		if r.rollback {
			return xerror.ErrExecutionCommitUnknown
		}
		return nil
	})
	if err == nil {
		return xerror.ErrExecutionCommitUnknown
	}
	return err
}

func TestExpiryActualSQLitePauseAndRAMOwnership(t *testing.T) {
	for _, mode := range []string{"commit", "commit recovered", "rollback unknown", "active attempt", "missing member", "stale boundary"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_STATE_HOME", root)
			t.Setenv("LOCALAPPDATA", root)
			store := sqlitestore.New()
			if err := store.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := store.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			}()
			repo := repositories.NewExecutionRepository(store)
			var writerRepository execution.CheckpointRepository = repo
			if mode == "commit recovered" || mode == "rollback unknown" {
				writerRepository = ambiguousRepository{ExecutionRepository: repo, rollback: mode == "rollback unknown"}
			}
			cp := services.NewCheckpointService(writerRepository, nil)
			defer func() { _ = cp.Shutdown(context.Background()) }()
			c, clock, owner := setup(t)
			c.writer = pauseFunc(cp.PauseGroup)
			groupID, first, second := c.Snapshot().ExecutionID, uuid.NewString(), uuid.NewString()
			admission := execution.Admission{RequestID: uuid.NewString(), RunGroupID: groupID, Now: clock.Now(), Devices: []execution.AdmissionDevice{
				{DeviceExecutionID: first, ADBSerial: "device-one", EntryNodeID: "set"},
				{DeviceExecutionID: second, ADBSerial: "device-two", EntryNodeID: "set"},
			}}
			if _, err := repo.Admit(t.Context(), admission); err != nil {
				t.Fatal(err)
			}
			live, err := variables.New(groupID, []string{first, second})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = live.Close(context.Background()) }()
			ref := execution.AttemptRef{RunGroupID: groupID, DeviceExecutionID: first, NodeID: "set", AttemptID: uuid.NewString()}
			if _, err := cp.BeginAttempt(t.Context(), execution.BeginAttemptRequest{AttemptRef: ref,
				ExpectedDeviceStatus: constants.ExecutionQueued, AttemptNumber: 1, MaxAttempts: 1, Now: clock.Now()}); err != nil {
				t.Fatal(err)
			}
			if _, err := live.Apply(t.Context(), execution.VariableWrite{AttemptRef: ref, Scope: constants.VariableScopeRunGroup, Name: "score", Value: json.RawMessage(`42`)},
				func(ctx context.Context) (execution.CheckpointResult, error) {
					return cp.FinishAttempt(ctx, execution.FinishAttemptRequest{AttemptRef: ref, NodeType: "flow.set_variable",
						Status: constants.ExecutionSucceeded, NextNodeID: "next", Output: json.RawMessage(`{}`), Now: clock.Now()})
				}); err != nil {
				t.Fatal(err)
			}
			if mode == "active attempt" {
				if _, err := cp.BeginAttempt(t.Context(), execution.BeginAttemptRequest{AttemptRef: execution.AttemptRef{
					RunGroupID: groupID, DeviceExecutionID: second, NodeID: "set", AttemptID: uuid.NewString()},
					ExpectedDeviceStatus: constants.ExecutionQueued, AttemptNumber: 1, MaxAttempts: 1, Now: clock.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			history, err := repo.Attempts(t.Context(), first, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			before, err := repo.SnapshotByID(t.Context(), groupID)
			if err != nil {
				t.Fatal(err)
			}
			raw := json.RawMessage(`{"private":"definition"}`)
			loaded := &productworkflow.Loaded{Document: &ir.Document{Nodes: []ir.Node{{Config: ir.Object{"value": raw}}}}}
			defer loaded.Close()
			owner.quiesce = func(ctx context.Context, id string, generation uint64, _ constants.ProductQuiesceMode) (Quiescence, error) {
				state, err := repo.ReadGroupControl(ctx, id)
				if err != nil {
					return Quiescence{}, err
				}
				r := Quiescence{ExecutionID: id, Generation: generation}
				for _, m := range state.Members {
					r.Members = append(r.Members, m.Boundary)
				}
				if mode == "missing member" {
					r.Members = r.Members[:1]
				} else if mode == "stale boundary" {
					r.Members[0].NextNodeID = "stale"
				}
				return r, nil
			}
			var releaseChecked bool
			owner.release = func(ctx context.Context, id string, _ uint64) error {
				snapshot, err := repo.SnapshotByID(ctx, id)
				if err != nil || snapshot.RunGroup.Status != constants.ExecutionPaused || snapshot.RunGroup.Summary.Paused != 2 {
					return errors.New("release requested before complete committed pause")
				}
				loaded.Close()
				releaseChecked = true
				return nil
			}
			clock.set(c.Snapshot().ExpiresAt)
			done := start(c, t.Context())
			defer c.End()
			shouldPass := mode == "commit" || mode == "commit recovered"
			if shouldPass {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				for c.Snapshot().Phase != constants.ProductAuthorizationExpired {
					select {
					case <-c.Changed():
					case err := <-done:
						t.Fatal("expiry failed", err)
					case <-ctx.Done():
						t.Fatal("expiry did not settle")
					}
				}
				end(t, c, done)
				if !releaseChecked || loaded.Document != nil {
					t.Fatal("committed expiry did not clean definition")
				}
			} else {
				if err := <-done; err == nil || releaseChecked || loaded.Document == nil {
					t.Fatal("unsafe database state released definition", err)
				}
				after, err := repo.SnapshotByID(t.Context(), groupID)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("failed expiry changed SQLite", err)
				}
			}
			afterHistory, err := repo.Attempts(t.Context(), first, 0, 0)
			if err != nil || !reflect.DeepEqual(history, afterHistory) {
				t.Fatal("expiry repeated/modified completed attempt", err)
			}
			value, present, err := live.Get(t.Context(), second, constants.VariableScopeRunGroup, "score")
			if err != nil || !present || string(value) != "42" {
				t.Fatal("expiry destroyed shared RAM", err)
			}
		})
	}
}
