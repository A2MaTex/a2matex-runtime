package attempts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/retry"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/routing"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/end"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

func storeFixture(t *testing.T, r Request) (*repositories.ExecutionRepository, *services.CheckpointService, *sqlitestore.Store) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	store := sqlitestore.New()
	if err := store.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	repo := repositories.NewExecutionRepository(store)
	_, err := repo.Admit(t.Context(), execution.Admission{RequestID: uuid.NewString(), RunGroupID: r.Ref.RunGroupID, Now: time.Now().Add(-time.Second),
		Devices: []execution.AdmissionDevice{{DeviceExecutionID: r.Ref.DeviceExecutionID, ADBSerial: r.ADBSerial, EntryNodeID: r.Node.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := services.NewCheckpointService(repo, nil)
	t.Cleanup(func() {
		if err := checkpoints.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return repo, checkpoints, store
}
func storedAttempts(t *testing.T, repo *repositories.ExecutionRepository, r Request) []execution.NodeAttempt {
	t.Helper()
	page, err := repo.Attempts(t.Context(), r.Ref.DeviceExecutionID, 0, constants.MaxAttemptPageSize)
	if err != nil {
		t.Fatal(err)
	}
	return page.Attempts
}
func assertStoredResult(t *testing.T, repo *repositories.ExecutionRepository, r Request, result Result) {
	t.Helper()
	rows := storedAttempts(t, repo, r)
	if len(rows) == 0 {
		t.Fatal("no attempt history")
	}
	row := rows[len(rows)-1]
	if row.ID != r.Ref.AttemptID || row.Status != result.Outcome.Status || row.Sequence != result.Sequence || string(row.Output) != string(result.Outcome.Output) {
		t.Fatalf("stored outcome: %+v, result %+v", row, result)
	}
	events, err := repo.ReplayEvents(t.Context(), 0, constants.MaxEventPageSize)
	if err != nil {
		t.Fatal(err)
	}
	terminal := 0
	for _, event := range events.Events {
		if event.Type != constants.EventAttemptStateChanged || event.NodeAttemptID != r.Ref.AttemptID {
			continue
		}
		var payload struct {
			Status        string
			AttemptNumber int
			Output        json.RawMessage
			Error         json.RawMessage
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Status == constants.ExecutionRunning {
			continue
		}
		terminal++
		if payload.Status != row.Status || payload.AttemptNumber != r.AttemptNumber || string(payload.Output) != string(row.Output) || string(payload.Error) != string(row.Error) {
			t.Fatalf("event disagrees with row: %s %+v", event.Payload, row)
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal events=%d", terminal)
	}
	snapshot, err := repo.SnapshotByID(t.Context(), r.Ref.RunGroupID)
	if err != nil {
		t.Fatal(err)
	}
	device := snapshot.DeviceExecutions[0]
	if result.NextNodeID != "" {
		if device.Status != constants.ExecutionRunning || device.NextNodeID == nil || *device.NextNodeID != result.NextNodeID {
			t.Fatalf("position: %+v", device)
		}
	} else {
		want := result.Outcome.Status
		if want == constants.ExecutionTimedOut {
			want = constants.ExecutionFailed
		}
		if device.Status != want || device.NextNodeID != nil || snapshot.RunGroup.Status != want {
			t.Fatalf("terminal snapshot: %+v", snapshot)
		}
	}
}

func TestSQLiteOrdinaryOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, kind, status, code string
		errorBranch              bool
		handler                  registry.Handler
	}{
		{"success", "flow.start", constants.ExecutionSucceeded, "", false, emptyHandler},
		{"end", "flow.end", constants.ExecutionSucceeded, "", false, end.Invoke},
		{"handler failure", "flow.start", constants.ExecutionFailed, constants.CommandFailed, true, func(context.Context, registry.Invocation) (map[string]any, error) {
			return nil, xerror.CommandError{Code: constants.CommandFailed, Message: "Command could not complete"}
		}},
		{"invalid output", "flow.start", constants.ExecutionFailed, constants.NodeOutputInvalid, false, func(context.Context, registry.Invocation) (map[string]any, error) {
			return map[string]any{"private": "not allowed"}, nil
		}},
		{"timeout", "flow.start", constants.ExecutionTimedOut, constants.NodeTimedOut, true, func(ctx context.Context, _ registry.Invocation) (map[string]any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := requestFor(tc.kind)
			if tc.errorBranch {
				r.Node.ErrorPolicy = constants.ErrorPolicyErrorBranch
			}
			if tc.name == "timeout" {
				r.Node.TimeoutMS = 1
			}
			repo, checkpoints, _ := storeFixture(t, r)
			calls := 0
			c := coordinator(t, dependencies(t, r, func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
				calls++
				rows := storedAttempts(t, repo, r)
				if len(rows) != 1 || rows[0].Status != constants.ExecutionRunning || rows[0].FinishedAt != nil {
					t.Fatalf("handler ran before durable begin: %+v", rows)
				}
				if call.ADBSerial != r.ADBSerial || call.StageScreenshot != nil {
					t.Fatalf("invocation: %+v", call)
				}
				return tc.handler(ctx, call)
			}, checkpoints))
			got, err := c.Run(t.Context(), r)
			if err != nil || !got.Began || !got.Finished || got.Outcome.Status != tc.status || calls != 1 || got.Retry != nil {
				t.Fatalf("attempt: %+v %v calls=%d", got, err, calls)
			}
			if tc.code != "" && (got.Outcome.Error == nil || got.Outcome.Error.Code != tc.code) {
				t.Fatalf("error: %+v", got.Outcome)
			}
			assertStoredResult(t, repo, r, got)
		})
	}
}

func TestSQLiteBindingFailureAndSafeRetries(t *testing.T) {
	t.Run("binding failure is checkpointed without invocation", func(t *testing.T) {
		r := requestFor("flow.delay")
		r.Node.Inputs = ir.Object{"durationMs": json.RawMessage(`{"source":"WORKFLOW_INPUT","inputName":"duration"}`)}
		r.Node.ErrorPolicy = constants.ErrorPolicyErrorBranch
		repo, checkpoints, _ := storeFixture(t, r)
		calls := 0
		c := coordinator(t, dependencies(t, r, func(context.Context, registry.Invocation) (map[string]any, error) { calls++; return nil, nil }, checkpoints))
		got, err := c.Run(t.Context(), r)
		if err != nil || calls != 0 || got.Outcome.Error == nil || got.Outcome.Error.Code != constants.CommandInputUnsupported || got.NextNodeID != "end" {
			t.Fatalf("binding failure: %+v %v calls=%d", got, err, calls)
		}
		assertStoredResult(t, repo, r, got)
	})
	t.Run("safe retry returns without waiting; next call requires fresh identity", func(t *testing.T) {
		r := requestFor("flow.start")
		r.Node.RetryPolicy = ir.RetryPolicy{MaxAttempts: 2}
		r.Node.ErrorPolicy = constants.ErrorPolicyErrorBranch
		repo, checkpoints, _ := storeFixture(t, r)
		calls := 0
		c := coordinator(t, dependencies(t, r, func(context.Context, registry.Invocation) (map[string]any, error) {
			calls++
			return nil, xerror.CommandError{Code: constants.CommandStartFailed, Message: "Command could not start", Retryable: true}
		}, checkpoints))
		first, err := c.Run(t.Context(), r)
		if err != nil || first.Retry == nil || first.NextNodeID != r.Node.ID || calls != 1 {
			t.Fatalf("retry proposal: %+v %v", first, err)
		}
		assertStoredResult(t, repo, r, first)
		if err := retry.Wait(t.Context(), first.Retry.DelayMS); err != nil {
			t.Fatal(err)
		}
		r.PreviousAttemptID = r.Ref.AttemptID
		r.Ref.AttemptID = uuid.NewString()
		r.AttemptNumber = 2
		r.ExpectedDeviceStatus = constants.ExecutionRunning
		second, err := c.Run(t.Context(), r)
		if err != nil || second.Sequence != 2 || second.Retry != nil || second.NextNodeID != "end" || calls != 2 {
			t.Fatalf("exhaustion: %+v %v", second, err)
		}
		assertStoredResult(t, repo, r, second)
		rows := storedAttempts(t, repo, r)
		if len(rows) != 2 || rows[0].Status != constants.ExecutionFailed || rows[1].Status != constants.ExecutionFailed {
			t.Fatalf("history: %+v", rows)
		}
		events, err := repo.ReplayEvents(t.Context(), 0, constants.MaxEventPageSize)
		if err != nil {
			t.Fatal(err)
		}
		scheduled := 0
		for _, event := range events.Events {
			if event.Type == constants.EventRetryScheduled {
				scheduled++
			}
		}
		if scheduled != 1 {
			t.Fatalf("retry events=%d", scheduled)
		}
	})
}

func TestSQLiteCommittedOutputHandoff(t *testing.T) {
	r := requestFor("flow.delay")
	r.Node.Inputs = ir.Object{"durationMs": json.RawMessage(`{"source":"LITERAL","value":7}`)}
	r.Values = bindings.Values{CommittedOutputs: map[string]ir.Object{}}
	repo, checkpoints, _ := storeFixture(t, r)
	var returned map[string]any
	d := dependencies(t, r, func(_ context.Context, call registry.Invocation) (map[string]any, error) {
		var value int
		if err := json.Unmarshal(call.Inputs["durationMs"], &value); err != nil {
			t.Fatal(err)
		}
		returned = map[string]any{"durationMs": value}
		return returned, nil
	}, checkpoints)
	consumer := r.Node
	consumer.ID = "consumer"
	consumer.Inputs = ir.Object{"durationMs": json.RawMessage(`{"source":"NODE_OUTPUT","nodeId":"node","field":"durationMs"}`)}
	routes, err := routing.New(t.Context(), ir.Document{Nodes: []ir.Node{r.Node, consumer, {ID: "end", Type: constants.EndNodeType, ErrorPolicy: constants.ErrorPolicyStop}},
		Edges: []ir.Edge{{SourceNodeID: "node", SourcePort: "success", TargetNodeID: "consumer"}, {SourceNodeID: "consumer", SourcePort: "success", TargetNodeID: "end"}}})
	if err != nil {
		t.Fatal(err)
	}
	d.Routes = routes
	c := coordinator(t, d)
	first, err := c.Run(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Values.CommittedOutputs) != 0 {
		t.Fatal("G9 mutated caller view")
	}
	returned["durationMs"] = 999
	if string(first.Outcome.Output) != `{"durationMs":7}` {
		t.Fatal("returned handler map mutated checked snapshot")
	}
	if _, err := d.Bindings.ResolveInputs(t.Context(), consumer, bindings.Values{}); err == nil {
		t.Fatal("unpublished output was visible")
	}
	var snapshot ir.Object
	if err := json.Unmarshal(first.Outcome.Output, &snapshot); err != nil {
		t.Fatal(err)
	}
	r.Values.CommittedOutputs["node"] = snapshot
	r.Ref.AttemptID = uuid.NewString()
	r.Ref.NodeID = consumer.ID
	r.Node = consumer
	r.ExpectedDeviceStatus = constants.ExecutionRunning
	second, err := c.Run(t.Context(), r)
	if err != nil || string(second.Outcome.Output) != `{"durationMs":7}` || second.Sequence != 2 {
		t.Fatalf("G4 handoff: %+v %v", second, err)
	}
	assertStoredResult(t, repo, r, second)
}

func TestSQLiteConcurrentAndRepeatedBegin(t *testing.T) {
	r := requestFor("flow.start")
	repo, checkpoints, _ := storeFixture(t, r)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	c := coordinator(t, dependencies(t, r, func(context.Context, registry.Invocation) (map[string]any, error) {
		calls.Add(1)
		close(entered)
		<-release
		return map[string]any{}, nil
	}, checkpoints))
	type reply struct {
		result Result
		err    error
	}
	done := make(chan reply, 1)
	go func() { result, err := c.Run(t.Context(), r); done <- reply{result, err} }()
	released, joined := false, false
	defer func() {
		if !released {
			close(release)
		}
		if !joined {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("attempt did not exit")
			}
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handler did not enter")
	}
	for _, fresh := range []bool{false, true} {
		other := r
		if fresh {
			other.Ref.AttemptID = uuid.NewString()
		}
		got, err := c.Run(t.Context(), other)
		if !errors.Is(err, xerror.ErrExecutionStaleState) {
			t.Fatalf("competing begin: %+v %v", got, err)
		}
		assertUnreleased(t, got, false)
	}
	close(release)
	released = true
	winner := <-done
	joined = true
	if winner.err != nil || !winner.result.Finished || calls.Load() != 1 {
		t.Fatalf("winner: %+v calls=%d", winner, calls.Load())
	}
	assertStoredResult(t, repo, r, winner.result)
	got, err := c.Run(t.Context(), r)
	if !errors.Is(err, xerror.ErrExecutionStaleState) {
		t.Fatalf("repeated begin: %+v %v", got, err)
	}
	if len(storedAttempts(t, repo, r)) != 1 || calls.Load() != 1 {
		t.Fatal("duplicate dispatch/history")
	}
}

func TestSQLiteWriteFailuresStopPublication(t *testing.T) {
	for _, phase := range []string{"begin", "finish"} {
		t.Run(phase, func(t *testing.T) {
			r := requestFor("flow.start")
			repo, checkpoints, store := storeFixture(t, r)
			calls := 0
			trigger := "BEFORE INSERT ON node_attempts"
			if phase == "finish" {
				trigger = "BEFORE UPDATE ON node_attempts"
			}
			if _, err := store.DB().ExecContext(t.Context(), "CREATE TRIGGER fail_checkpoint "+trigger+" BEGIN SELECT RAISE(ABORT, 'test write fault'); END"); err != nil {
				t.Fatal(err)
			}
			c := coordinator(t, dependencies(t, r, func(context.Context, registry.Invocation) (map[string]any, error) {
				calls++
				return map[string]any{}, nil
			}, checkpoints))
			got, err := c.Run(t.Context(), r)
			if err == nil {
				t.Fatal("write failure accepted")
			}
			assertUnreleased(t, got, phase == "finish")
			rows := storedAttempts(t, repo, r)
			snapshot, readErr := repo.SnapshotByID(t.Context(), r.Ref.RunGroupID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if phase == "begin" {
				if calls != 0 || len(rows) != 0 || snapshot.Cursor != "2" {
					t.Fatalf("failed begin: calls=%d rows=%+v snapshot=%+v", calls, rows, snapshot)
				}
			} else {
				if calls != 1 || len(rows) != 1 || rows[0].Status != constants.ExecutionRunning || rows[0].Output != nil || snapshot.Cursor != "4" || !errors.Is(err, xerror.ErrCheckpointFatal) {
					t.Fatalf("failed finish: %+v %v rows=%+v snapshot=%+v", got, err, rows, snapshot)
				}
			}
		})
	}
}

type uncertainRepository struct {
	*repositories.ExecutionRepository
	at, writes, readbacks int
	commit                bool
}

func (r *uncertainRepository) WithCheckpointTx(ctx context.Context, operation func(execution.CheckpointTx) error) error {
	r.writes++
	if r.writes != r.at {
		return r.ExecutionRepository.WithCheckpointTx(ctx, operation)
	}
	err := r.ExecutionRepository.WithCheckpointTx(ctx, func(tx execution.CheckpointTx) error {
		if err := operation(tx); err != nil {
			return err
		}
		if !r.commit {
			return xerror.ErrExecutionCommitUnknown
		}
		return nil
	})
	if err != nil {
		return err
	}
	return xerror.ErrExecutionCommitUnknown
}
func (r *uncertainRepository) ReadCheckpoint(ctx context.Context, ref execution.AttemptRef) (execution.CheckpointState, int64, error) {
	r.readbacks++
	return r.ExecutionRepository.ReadCheckpoint(ctx, ref)
}

func TestSQLiteUncertainCommitReadback(t *testing.T) {
	for _, at := range []int{1, 2} {
		for _, commit := range []bool{false, true} {
			name := map[int]string{1: "begin", 2: "finish"}[at] + map[bool]string{false: "/rolled back", true: "/committed"}[commit]
			t.Run(name, func(t *testing.T) {
				r := requestFor("flow.start")
				repo, _, _ := storeFixture(t, r)
				uncertain := &uncertainRepository{ExecutionRepository: repo, at: at, commit: commit}
				checkpoints := services.NewCheckpointService(uncertain, nil)
				t.Cleanup(func() {
					if err := checkpoints.Shutdown(context.Background()); err != nil {
						t.Error(err)
					}
				})
				calls := 0
				c := coordinator(t, dependencies(t, r, func(context.Context, registry.Invocation) (map[string]any, error) {
					calls++
					return map[string]any{}, nil
				}, checkpoints))
				got, err := c.Run(t.Context(), r)
				if uncertain.readbacks != 1 {
					t.Fatalf("readbacks=%d", uncertain.readbacks)
				}
				if commit {
					if err != nil || calls != 1 || uncertain.writes != 2 {
						t.Fatalf("confirmed commit: %+v %v calls=%d writes=%d", got, err, calls, uncertain.writes)
					}
					assertStoredResult(t, repo, r, got)
				} else {
					if !errors.Is(err, xerror.ErrCheckpointFatal) || uncertain.writes != at {
						t.Fatalf("unknown commit: %+v %v writes=%d", got, err, uncertain.writes)
					}
					assertUnreleased(t, got, at == 2)
					want := 0
					if at == 2 {
						want = 1
					}
					if calls != want {
						t.Fatalf("unsafe calls=%d", calls)
					}
				}
			})
		}
	}
}

func TestSQLiteCancellationUsesBoundedFinish(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unaccepted", true: "accepted"}[accepted], func(t *testing.T) {
			r := requestFor("flow.start")
			repo, checkpoints, _ := storeFixture(t, r)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stopped := false
			r.Control = func() Control {
				return Control{GroupCancellation: accepted && stopped, ShutdownDeadline: time.Now().Add(time.Second)}
			}
			c := coordinator(t, dependencies(t, r, func(context.Context, registry.Invocation) (map[string]any, error) {
				stopped = true
				cancel()
				return map[string]any{}, nil
			}, checkpoints))
			got, err := c.Run(ctx, r)
			if accepted {
				if err != nil || got.Outcome.Status != constants.ExecutionCancelled {
					t.Fatalf("accepted: %+v %v", got, err)
				}
				assertStoredResult(t, repo, r, got)
			} else {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				assertUnreleased(t, got, true)
				rows := storedAttempts(t, repo, r)
				if len(rows) != 1 || rows[0].Status != constants.ExecutionRunning {
					t.Fatalf("unsettled history: %+v", rows)
				}
			}
		})
	}
}

// Finish twice simulates another caller finalizing the same active attempt
// before this caller obtains its own acknowledgement.
type duplicateFinish struct{ *services.CheckpointService }

func (d duplicateFinish) FinishAttempt(ctx context.Context, r execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
	if _, err := d.CheckpointService.FinishAttempt(ctx, r); err != nil {
		return execution.CheckpointResult{}, err
	}
	return d.CheckpointService.FinishAttempt(ctx, r)
}
func TestSQLiteDuplicateFinishDoesNotReleaseProposedResult(t *testing.T) {
	r := requestFor("flow.start")
	repo, checkpoints, _ := storeFixture(t, r)
	got, err := coordinator(t, dependencies(t, r, emptyHandler, duplicateFinish{checkpoints})).Run(t.Context(), r)
	if !errors.Is(err, xerror.ErrAttemptInvalid) {
		t.Fatalf("duplicate finish: %+v %v", got, err)
	}
	assertUnreleased(t, got, true)
	rows := storedAttempts(t, repo, r)
	if len(rows) != 1 || rows[0].Status != constants.ExecutionSucceeded {
		t.Fatalf("duplicate changed durable history: %+v", rows)
	}
	events, err := repo.ReplayEvents(t.Context(), 0, constants.MaxEventPageSize)
	if err != nil {
		t.Fatal(err)
	}
	terminal := 0
	for _, event := range events.Events {
		if event.NodeAttemptID == r.Ref.AttemptID && strings.Contains(string(event.Payload), "SUCCEEDED") {
			terminal++
		}
	}
	if terminal != 1 {
		t.Fatalf("duplicate completion events=%d", terminal)
	}
}
func TestSQLiteExpiredShutdownDeadlineStopsFinish(t *testing.T) {
	r := requestFor("flow.start")
	repo, checkpoints, _ := storeFixture(t, r)
	r.Control = func() Control { return Control{ShutdownDeadline: time.Now().Add(-time.Second)} }
	got, err := coordinator(t, dependencies(t, r, emptyHandler, checkpoints)).Run(t.Context(), r)
	if !errors.Is(err, xerror.ErrCheckpointFatal) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired finish: %+v %v", got, err)
	}
	assertUnreleased(t, got, true)
	rows := storedAttempts(t, repo, r)
	if len(rows) != 1 || rows[0].Status != constants.ExecutionRunning || rows[0].Output != nil {
		t.Fatalf("expired finish changed attempt: %+v", rows)
	}
}
