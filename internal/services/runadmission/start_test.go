package runadmission_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/variables"
	"github.com/A2MaTex/a2matex-runtime/internal/services/runadmission"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type startAdmitFunc func(context.Context, runadmission.Request) (runadmission.Result, error)

func (f startAdmitFunc) Admit(ctx context.Context, r runadmission.Request) (runadmission.Result, error) {
	return f(ctx, r)
}

type startOwner struct {
	trace       *[]string
	dispatchErr error
	dispatch    func(string, *runner.Runner, []runner.Request) error
}

func (o startOwner) BeginStart(ctx context.Context) (context.Context, func(), error) {
	*o.trace = append(*o.trace, "reserve")
	return ctx, func() { *o.trace = append(*o.trace, "release") }, nil
}
func (o startOwner) DispatchGroup(id string, worker *runner.Runner, devices []runner.Request) error {
	*o.trace = append(*o.trace, "dispatch")
	if o.dispatch != nil {
		return o.dispatch(id, worker, devices)
	}
	return o.dispatchErr
}

func TestStartTransfersPreparedGroupWithoutAllocatingRAM(t *testing.T) {
	for _, name := range []string{"accepted", "dispatch failure", "snapshot failure"} {
		t.Run(name, func(t *testing.T) {
			f := fixture(t, 3)
			created, err := service(t, f.deps).Admit(t.Context(), f.req)
			if err != nil {
				t.Fatal(err)
			}
			var trace []string
			var transferred []runner.Request
			owner := startOwner{trace: &trace, dispatch: func(id string, worker *runner.Runner, devices []runner.Request) error {
				transferred = devices
				if id != created.RunGroupID || worker != created.Work.Runner {
					t.Fatal("incorrect group handoff")
				}
				if name == "dispatch failure" {
					return xerror.ErrExecutionLifecycleInvalid
				}
				return nil
			}}
			reader := repositoryStub{snapshot: func(ctx context.Context, id string) (execution.Snapshot, error) {
				if name == "snapshot failure" {
					return execution.Snapshot{}, xerror.ErrExecutionPersistence
				}
				return f.repo.SnapshotByID(ctx, id)
			}}
			starter, err := runadmission.NewStarter(startAdmitFunc(func(context.Context, runadmission.Request) (runadmission.Result, error) { return created, nil }), reader, owner)
			if err != nil {
				t.Fatal(err)
			}
			_, err = starter.Start(t.Context(), f.req)
			if (name == "accepted") != (err == nil) {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(transferred, created.Work.Devices) || len(transferred) != 3 ||
				transferred[0].VariableStore != nil || transferred[0].Variables.Lookup != nil {
				t.Fatal("start must transfer prepared work without installing RAM authority")
			}
		})
	}
}
func (o startOwner) Fail(error) { *o.trace = append(*o.trace, "fail") }

func TestMultiDeviceAdmissionRetainsReplay(t *testing.T) {
	f := fixture(t, 2)
	s := service(t, f.deps)
	created, err := s.Admit(t.Context(), f.req)
	if err != nil || created.Work == nil || len(created.Work.Devices) != 2 {
		t.Fatalf("complete admission: %+v %v", created, err)
	}
	replacement := f.req
	replacement.Devices = nil
	replay, err := s.Admit(t.Context(), replacement)
	if err != nil || replay.Disposition != constants.AdmissionExisting || replay.Work != nil {
		t.Fatalf("replacement semantics: %+v %v", replay, err)
	}
	requireCounts(t, f, 1, 2, 3)
}

func TestStartHandoffOrderingAndFailureFencing(t *testing.T) {
	for _, name := range []string{"created", "disconnect after commit", "snapshot error", "handoff error", "uncertain commit", "broken work", "injected view", "injected store", "injected boundary", "later group", "later duplicate", "existing", "busy"} {
		t.Run(name, func(t *testing.T) {
			f := fixture(t, 3)
			created, err := service(t, f.deps).Admit(t.Context(), f.req)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var trace []string
			owner := startOwner{trace: &trace}
			if name == "handoff error" {
				owner.dispatchErr = xerror.ErrExecutionLifecycleInvalid
			}
			admitter := startAdmitFunc(func(context.Context, runadmission.Request) (runadmission.Result, error) {
				trace = append(trace, "admit")
				switch name {
				case "disconnect after commit":
					cancel()
				case "uncertain commit":
					return runadmission.Result{}, xerror.ErrExecutionCommitUnknown
				case "broken work":
					return runadmission.Result{Disposition: constants.AdmissionCreated}, nil
				case "injected boundary":
					created.Work.Devices[2].BeforeAttempt = func(context.Context, execution.Boundary) (bool, error) { return false, nil }
				case "injected view":
					created.Work.Devices[2].Variables.RunGroupID = created.RunGroupID
				case "injected store":
					store, err := variables.New(created.RunGroupID, []string{created.Work.Devices[0].DeviceExecutionID})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := store.Close(context.Background()); err != nil {
							t.Error(err)
						}
					})
					created.Work.Devices[2].VariableStore = store
				case "later group":
					created.Work.Devices[2].RunGroupID = created.Work.Devices[0].DeviceExecutionID
				case "later duplicate":
					created.Work.Devices[2].DeviceExecutionID = created.Work.Devices[0].DeviceExecutionID
				case "existing":
					s, _ := f.repo.SnapshotByID(t.Context(), created.RunGroupID)
					return runadmission.Result{Disposition: constants.AdmissionExisting, Snapshot: &s}, nil
				case "busy":
					return runadmission.Result{Disposition: constants.AdmissionBusy}, nil
				}
				return created, nil
			})
			reader := repositoryStub{snapshot: func(ctx context.Context, id string) (execution.Snapshot, error) {
				trace = append(trace, "snapshot")
				if name == "snapshot error" {
					return execution.Snapshot{}, xerror.ErrExecutionPersistence
				}
				return f.repo.SnapshotByID(ctx, id)
			}}
			s, err := runadmission.NewStarter(admitter, reader, owner)
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.Start(ctx, f.req)
			want := []string{"reserve", "admit", "dispatch", "release", "snapshot"}
			switch name {
			case "handoff error":
				want = []string{"reserve", "admit", "dispatch", "fail", "release"}
			case "uncertain commit", "broken work", "injected view", "injected store", "injected boundary", "later group", "later duplicate":
				want = []string{"reserve", "admit", "fail", "release"}
			case "existing", "busy":
				want = []string{"reserve", "admit", "release"}
			}
			if !reflect.DeepEqual(trace, want) {
				t.Fatalf("ordering: %v want %v", trace, want)
			}
			if name == "created" || name == "existing" || name == "busy" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || result.Snapshot != nil {
				t.Fatalf("failure result: %+v %v", result, err)
			}
		})
	}
}

func TestStartStopBetweenCommitAndHandoffSettlesQueuedWork(t *testing.T) {
	f := fixture(t, 3)
	wrapped := wrappedRepository(f)
	wrapped.admit = func(ctx context.Context, a execution.Admission) (execution.AdmissionResult, error) {
		r, err := f.repo.Admit(ctx, a)
		if err == nil {
			f.lifetime.PrepareStop(constants.ExecutionStopIntentional, time.Now().Add(time.Second))
		}
		return r, err
	}
	f.deps.Repository = wrapped
	s, err := runadmission.NewStarter(service(t, f.deps), f.repo, f.lifetime)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Start(t.Context(), f.req)
	if err != nil || result.Disposition != constants.AdmissionCreated {
		t.Fatalf("confirmed stop race: %+v %v", result, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := f.lifetime.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.repo.SnapshotByID(t.Context(), result.Snapshot.RunGroup.ID)
	if err != nil || snapshot.RunGroup.Status != constants.ExecutionCancelled || f.calls.Load() != 0 {
		t.Fatalf("settlement: %+v %v", snapshot, err)
	}
	requireCounts(t, f, 1, 3, 8)
}
