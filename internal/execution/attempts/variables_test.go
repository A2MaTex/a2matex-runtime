package attempts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/routing"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/variables"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/setvariable"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

func variableRequest(t *testing.T) Request {
	t.Helper()
	r := requestFor(constants.SetVariableNodeType)
	r.Node.Config = ir.Object{"name": []byte(`"score"`), "scope": []byte(`"FLOW"`)}
	r.Node.Inputs = ir.Object{"value": []byte(`{"source":"LITERAL","value":30}`)}
	store, err := variables.New(r.Ref.RunGroupID, []string{r.Ref.DeviceExecutionID})
	if err != nil {
		t.Fatal(err)
	}
	r.VariableStore = store
	r.Values.Variables = bindings.VariableView{RunGroupID: r.Ref.RunGroupID, DeviceExecutionID: r.Ref.DeviceExecutionID,
		Lookup: func(ctx context.Context, scope constants.VariableScope, name string) (json.RawMessage, bool, error) {
			return store.Get(ctx, r.Ref.DeviceExecutionID, scope, name)
		}}
	t.Cleanup(func() {
		if err := store.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return r
}

func seedVariable(t *testing.T, r Request, name, value string) {
	t.Helper()
	write := execution.VariableWrite{AttemptRef: r.Ref, Scope: constants.VariableScopeFlow, Name: name, Value: []byte(value)}
	_, err := r.VariableStore.Apply(t.Context(), write, func(context.Context) (execution.CheckpointResult, error) {
		return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "1"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAssignmentSQLiteCreateReplaceAndReadOnce(t *testing.T) {
	r := variableRequest(t)
	doc := ir.Document{}
	for i := range 4 {
		n := r.Node
		n.ID = fmt.Sprintf("assign_%d", i)
		doc.Nodes = append(doc.Nodes, n)
		next := "end"
		if i < 3 {
			next = fmt.Sprintf("assign_%d", i+1)
		}
		doc.Edges = append(doc.Edges, ir.Edge{SourceNodeID: n.ID, SourcePort: constants.NodeSuccessPort, TargetNodeID: next})
	}
	doc.Nodes = append(doc.Nodes, ir.Node{ID: "end", Type: constants.EndNodeType, ErrorPolicy: constants.ErrorPolicyStop})
	routes, err := routing.New(t.Context(), doc)
	if err != nil {
		t.Fatal(err)
	}
	r.Ref.NodeID, r.Node.ID = "assign_0", "assign_0"
	repo, checkpoints, _ := storeFixture(t, r)
	for i, tc := range []struct{ scope, value string }{{"FLOW", "30"}, {"FLOW", `"private scalar"`}, {"RUN_GROUP", "false"}} {
		r.Ref.AttemptID = uuid.NewString()
		r.Ref.NodeID, r.Node.ID = fmt.Sprintf("assign_%d", i), fmt.Sprintf("assign_%d", i)
		r.Node.Config["scope"] = []byte(`"` + tc.scope + `"`)
		r.Node.Inputs["value"] = []byte(`{"source":"WORKFLOW_INPUT","inputName":"score"}`)
		r.Values.WorkflowInputs = ir.Object{"score": []byte(tc.value)}
		if i > 0 {
			r.ExpectedDeviceStatus = constants.ExecutionRunning
		}
		deps := dependencies(t, r, setvariable.Invoke, checkpoints)
		deps.Routes = routes
		got, err := coordinator(t, deps).Run(t.Context(), r)
		if err != nil || !got.Finished || got.Outcome.Status != constants.ExecutionSucceeded || string(got.Outcome.Output) != "{}" {
			t.Fatal(got, err)
		}
		value, found, err := r.VariableStore.Get(t.Context(), r.Ref.DeviceExecutionID, constants.VariableScope(tc.scope), "score")
		if err != nil || !found || string(value) != tc.value {
			t.Fatal(string(value), found, err)
		}
		assertStoredResult(t, repo, r, got)
	}
	// Read a previous assignment once, then replace it from that resolved snapshot.
	reads := 0
	lookup := r.Values.Variables.Lookup
	r.Values.Variables.Lookup = func(ctx context.Context, scope constants.VariableScope, name string) (json.RawMessage, bool, error) {
		reads++
		return lookup(ctx, scope, name)
	}
	r.Ref.AttemptID = uuid.NewString()
	r.Ref.NodeID, r.Node.ID = "assign_3", "assign_3"
	r.Node.Config["scope"] = []byte(`"FLOW"`)
	r.Node.Inputs["value"] = []byte(`{"source":"GLOBAL_VARIABLE","name":"score"}`)
	deps := dependencies(t, r, setvariable.Invoke, checkpoints)
	deps.Routes = routes
	got, err := coordinator(t, deps).Run(t.Context(), r)
	if err != nil || !got.Finished || reads != 1 {
		t.Fatal(got, reads, err)
	}
	events, err := repo.ReplayEvents(t.Context(), 0, constants.MaxEventPageSize)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events.Events {
		if strings.Contains(string(event.Payload), "private scalar") || strings.Contains(string(event.Payload), `"scope"`) {
			t.Fatal("assignment leaked into event")
		}
	}
}

func TestAssignmentQuotaFailureFinalizesOnceWithoutMutation(t *testing.T) {
	for _, quota := range []string{"names", "bytes", "replacement bytes"} {
		t.Run(quota, func(t *testing.T) {
			r := variableRequest(t)
			if quota == "names" {
				for i := range constants.MaxVariablesPerScope {
					seedVariable(t, r, fmt.Sprintf("v%d", i), "1")
				}
			} else {
				value := `"` + strings.Repeat("a", constants.MaxVariableValueBytes-2) + `"`
				for i := range 15 {
					seedVariable(t, r, fmt.Sprintf("v%d", i), value)
				}
				r.Node.Inputs["value"] = []byte(`{"source":"LITERAL","value":` + value + `}`)
			}
			r.Node.ErrorPolicy = constants.ErrorPolicyErrorBranch
			next := "end"
			if quota == "replacement bytes" {
				seedVariable(t, r, "score", "1")
				r.Node.ErrorPolicy, next = constants.ErrorPolicyStop, ""
			}
			r.Node.RetryPolicy.MaxAttempts = 3
			cp := &checkpointStub{}
			got, err := coordinator(t, dependencies(t, r, setvariable.Invoke, cp)).Run(t.Context(), r)
			if err != nil || cp.finishes != 1 || got.Outcome.Status != constants.ExecutionFailed || got.NextNodeID != next || got.Retry != nil || got.Outcome.Error.Code != constants.CommandInputUnsupported {
				t.Fatal(got, err, cp.finishes)
			}
			value, found, err := r.VariableStore.Get(t.Context(), r.Ref.DeviceExecutionID, constants.VariableScopeFlow, "score")
			if err != nil || found != (quota == "replacement bytes") || (found && string(value) != "1") {
				t.Fatal(string(value), found, err)
			}
			if value, found, err := r.VariableStore.Get(t.Context(), r.Ref.DeviceExecutionID, constants.VariableScopeFlow, "v0"); !found || err != nil || len(value) == 0 {
				t.Fatal(found, err)
			}
		})
	}
}

func TestAssignmentUnconfirmedFinishNeverPublishesOrFinishesTwice(t *testing.T) {
	for _, name := range []string{"uncertain", "stale", "finalized", "wrong sequence", "invalid cursor"} {
		t.Run(name, func(t *testing.T) {
			r := variableRequest(t)
			cp := &checkpointStub{finish: func(context.Context, execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
				result := execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "5"}
				switch name {
				case "uncertain":
					return execution.CheckpointResult{}, xerror.ErrExecutionCommitUnknown
				case "stale":
					result.Disposition = constants.CheckpointStale
				case "finalized":
					result.Disposition = constants.CheckpointAlreadyFinalized
				case "wrong sequence":
					result.Sequence = 2
				case "invalid cursor":
					result.Cursor = "bad"
				}
				return result, nil
			}}
			got, err := coordinator(t, dependencies(t, r, setvariable.Invoke, cp)).Run(t.Context(), r)
			if err == nil || cp.finishes != 1 {
				t.Fatal(got, err, cp.finishes)
			}
			assertUnreleased(t, got, true)
			_, found, readErr := r.VariableStore.Get(t.Context(), r.Ref.DeviceExecutionID, constants.VariableScopeFlow, "score")
			if found || (name != "stale" && name != "finalized" && !errors.Is(readErr, xerror.ErrVariableUnavailable)) {
				t.Fatal(found, readErr)
			}
		})
	}
}

func TestAssignmentReaderWaitsForCheckpointAndPublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := variableRequest(t)
		seedVariable(t, r, "score", "1")
		finishing, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		cp := &checkpointStub{finish: func(context.Context, execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
			close(finishing)
			<-release
			cancel() // Confirmation still publishes when owner cancellation arrives during settlement.
			return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "5"}, nil
		}}
		c := coordinator(t, dependencies(t, r, setvariable.Invoke, cp))
		go func() {
			defer close(done)
			if got, err := c.Run(ctx, r); err != nil || !got.Finished {
				t.Error(got, err)
			}
		}()
		<-finishing
		read := make(chan string, 1)
		go func() {
			value, _, err := r.VariableStore.Get(t.Context(), r.Ref.DeviceExecutionID, constants.VariableScopeFlow, "score")
			if err != nil {
				t.Error(err)
			}
			read <- string(value)
		}()
		synctest.Wait()
		select {
		case <-read:
			t.Fatal("reader crossed checkpoint gap")
		default:
		}
		close(release)
		<-done
		if got := <-read; got != "30" {
			t.Fatal(got)
		}
	})
}

func TestAssignmentStopWhileWaitingForStore(t *testing.T) {
	for _, name := range []string{"owner loss", "intentional", "control only"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := variableRequest(t)
				var accepted atomic.Bool
				r.Control = func() Control { return Control{GroupCancellation: accepted.Load()} }
				held, release, holdingDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
				go func() {
					defer close(holdingDone)
					_, err := r.VariableStore.Apply(t.Context(), execution.VariableWrite{AttemptRef: r.Ref, Scope: constants.VariableScopeFlow, Name: "other", Value: []byte("1")}, func(context.Context) (execution.CheckpointResult, error) {
						close(held)
						<-release
						return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "1"}, nil
					})
					if err != nil {
						t.Error(err)
					}
				}()
				<-held
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				cp := &checkpointStub{}
				handled := make(chan struct{})
				c := coordinator(t, dependencies(t, r, func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
					defer close(handled)
					return setvariable.Invoke(ctx, call)
				}, cp))
				done := make(chan struct{})
				var got Result
				var runErr error
				go func() { defer close(done); got, runErr = c.Run(ctx, r) }()
				<-handled
				synctest.Wait()
				accepted.Store(name != "owner loss")
				if name != "control only" {
					cancel()
				}
				close(release)
				<-holdingDone
				<-done
				if name == "owner loss" {
					if !errors.Is(runErr, context.Canceled) || cp.finishes != 0 {
						t.Fatal(got, runErr, cp.finishes)
					}
				} else if runErr != nil || cp.finishes != 1 || cp.last.Status != constants.ExecutionCancelled || !got.Finished {
					t.Fatal(got, runErr, cp.last)
				}
				if _, found, _ := r.VariableStore.Get(t.Context(), r.Ref.DeviceExecutionID, constants.VariableScopeFlow, "score"); found {
					t.Fatal("cancelled assignment published")
				}
			})
		})
	}
}

func TestAssignmentSourcesAndNonSuccessDoNotMutate(t *testing.T) {
	for _, name := range []string{"literal", "workflow", "node output", "flow", "global", "missing", "handler error", "bad output", "deadline", "no writer", "foreign writer", "closed writer"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := variableRequest(t)
				seedVariable(t, r, "score", "0")
				cp, calls := &checkpointStub{}, 0
				handler := registry.Handler(func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
					calls++
					switch name {
					case "handler error":
						return nil, xerror.CommandError{Code: constants.CommandInputUnsupported, Message: "controlled invalid value"}
					case "bad output":
						return map[string]any{"value": 1}, nil
					case "deadline":
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return setvariable.Invoke(ctx, call)
				})
				want, wantCode := "30", ""
				switch name {
				case "workflow":
					r.Node.Inputs["value"] = []byte(`{"source":"WORKFLOW_INPUT","inputName":"score"}`)
					r.Values.WorkflowInputs = ir.Object{"score": []byte("30")}
				case "node output":
					r.Node.Inputs["value"] = []byte(`{"source":"NODE_OUTPUT","nodeId":"producer","field":"durationMs"}`)
					r.Values.CommittedOutputs = map[string]ir.Object{"producer": {"durationMs": []byte("30")}}
				case "flow":
					r.Node.Inputs["value"] = []byte(`{"source":"FLOW_VARIABLE","name":"score"}`)
					want = "0"
				case "global":
					r.Node.Config["scope"] = []byte(`"RUN_GROUP"`)
					r.Node.Inputs["value"] = []byte(`{"source":"FLOW_VARIABLE","name":"score"}`)
					want = "0"
				case "missing":
					r.Node.Inputs["value"] = []byte(`{"source":"FLOW_VARIABLE","name":"missing"}`)
					wantCode = constants.CommandInputUnsupported
				case "handler error":
					wantCode = constants.CommandInputUnsupported
				case "bad output":
					wantCode = constants.NodeOutputInvalid
				case "deadline":
					wantCode = constants.NodeTimedOut
				case "no writer":
					r.VariableStore = nil
				case "foreign writer":
					foreign, err := variables.New(uuid.NewString(), []string{uuid.NewString()})
					if err != nil {
						t.Fatal(err)
					}
					r.VariableStore = foreign
				case "closed writer":
					if err := r.VariableStore.Close(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				got, err := coordinator(t, dependencies(t, r, handler, cp)).Run(t.Context(), r)
				if strings.Contains(name, "writer") {
					if err == nil || cp.finishes != 0 || (name == "no writer" && (cp.begins != 0 || calls != 0)) {
						t.Fatal(got, err, cp)
					}
					return
				}
				if err != nil || !got.Finished || cp.finishes != 1 {
					t.Fatal(got, err, cp)
				}
				if wantCode != "" {
					if got.Outcome.Error == nil || got.Outcome.Error.Code != wantCode || (name == "missing" && calls != 0) {
						t.Fatal(got, calls)
					}
					want = "0"
				} else if got.Outcome.Status != constants.ExecutionSucceeded || calls != 1 {
					t.Fatal(got, calls)
				}
				scope := constants.VariableScopeFlow
				if name == "global" {
					scope = constants.VariableScopeRunGroup
				}
				value, found, err := r.VariableStore.Get(t.Context(), r.Ref.DeviceExecutionID, scope, "score")
				if err != nil || !found || string(value) != want {
					t.Fatal(string(value), found, err)
				}
			})
		})
	}
}

type uncertainVariableRepository struct {
	*repositories.ExecutionRepository
	commit            bool
	writes, readbacks int
}

func (r *uncertainVariableRepository) WithCheckpointTx(ctx context.Context, operation func(execution.CheckpointTx) error) error {
	r.writes++
	if r.writes == 1 {
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

func (r *uncertainVariableRepository) ReadCheckpoint(ctx context.Context, ref execution.AttemptRef) (execution.CheckpointState, int64, error) {
	r.readbacks++
	return r.ExecutionRepository.ReadCheckpoint(ctx, ref)
}

func TestAssignmentRealSQLiteFaultBoundary(t *testing.T) {
	for _, name := range []string{"confirmed readback", "unconfirmed rollback", "transaction rollback"} {
		t.Run(name, func(t *testing.T) {
			r := variableRequest(t)
			repo, normal, db := storeFixture(t, r)
			var cp Checkpoints = normal
			fault := &uncertainVariableRepository{ExecutionRepository: repo, commit: name == "confirmed readback"}
			if name == "transaction rollback" {
				if _, err := db.DB().ExecContext(t.Context(), `CREATE TRIGGER fail_variable BEFORE UPDATE ON node_attempts WHEN NEW.status='SUCCEEDED' BEGIN SELECT RAISE(ABORT, 'controlled rollback'); END`); err != nil {
					t.Fatal(err)
				}
			} else {
				cp = services.NewCheckpointService(fault, nil)
			}
			got, err := coordinator(t, dependencies(t, r, setvariable.Invoke, cp)).Run(t.Context(), r)
			value, found, readErr := r.VariableStore.Get(t.Context(), r.Ref.DeviceExecutionID, constants.VariableScopeFlow, "score")
			rows := storedAttempts(t, repo, r)
			if len(rows) != 1 {
				t.Fatal(rows)
			}
			if name == "confirmed readback" {
				if err != nil || readErr != nil || !found || string(value) != "30" || !got.Finished || rows[0].Status != constants.ExecutionSucceeded || fault.readbacks != 1 {
					t.Fatal(got, err, string(value), found, readErr)
				}
			} else if err == nil || found || !errors.Is(readErr, xerror.ErrVariableUnavailable) || got.Finished || rows[0].Status != constants.ExecutionRunning {
				t.Fatal(got, err, rows, found, readErr)
			}
			if name != "transaction rollback" && fault.writes != 2 {
				t.Fatal("checkpoint was retried", fault.writes)
			}
		})
	}
}
