package runadmission_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/services/runadmission"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type singleAdmitFunc func(context.Context, runadmission.Request) (runadmission.Result, error)

func (f singleAdmitFunc) AdmitSingleDevice(ctx context.Context, r runadmission.Request) (runadmission.Result, error) {
	return f(ctx, r)
}

type startOwner struct {
	trace       *[]string
	dispatchErr error
}

func (o startOwner) BeginStart(ctx context.Context) (context.Context, func(), error) {
	*o.trace = append(*o.trace, "reserve")
	return ctx, func() { *o.trace = append(*o.trace, "release") }, nil
}
func (o startOwner) Dispatch(*runner.Runner, runner.Request) error {
	*o.trace = append(*o.trace, "dispatch")
	return o.dispatchErr
}
func (o startOwner) Fail(error) { *o.trace = append(*o.trace, "fail") }

func TestSingleDeviceAdmissionRetainsReplayAndInternalCapacity(t *testing.T) {
	f := fixture(t, 2)
	s := service(t, f.deps)
	if _, err := s.AdmitSingleDevice(t.Context(), f.req); !errors.Is(err, xerror.ErrRunAdmissionInvalid) {
		t.Fatalf("multiple devices: %v", err)
	}
	requireCounts(t, f, 0, 0, 0)
	request := f.req
	request.Devices = request.Devices[:1]
	created, err := s.AdmitSingleDevice(t.Context(), request)
	if err != nil || created.Work == nil {
		t.Fatalf("single admission: %+v %v", created, err)
	}
	replay, err := s.AdmitSingleDevice(t.Context(), f.req)
	if err != nil || replay.Disposition != constants.AdmissionExisting || replay.Work != nil {
		t.Fatalf("replacement device semantics: %+v %v", replay, err)
	}
	requireCounts(t, f, 1, 1, 2)
}

func TestStartHandoffOrderingAndFailureFencing(t *testing.T) {
	for _, name := range []string{"created", "disconnect after commit", "snapshot error", "handoff error", "uncertain commit", "broken work", "existing", "busy"} {
		t.Run(name, func(t *testing.T) {
			f := fixture(t, 1)
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
			admitter := singleAdmitFunc(func(context.Context, runadmission.Request) (runadmission.Result, error) {
				trace = append(trace, "admit")
				switch name {
				case "disconnect after commit":
					cancel()
				case "uncertain commit":
					return runadmission.Result{}, xerror.ErrExecutionCommitUnknown
				case "broken work":
					return runadmission.Result{Disposition: constants.AdmissionCreated}, nil
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
			case "uncertain commit", "broken work":
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
	f := fixture(t, 1)
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
	requireCounts(t, f, 1, 1, 4)
}
