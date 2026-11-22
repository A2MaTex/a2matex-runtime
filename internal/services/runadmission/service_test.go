package runadmission_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outputs"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/services/runadmission"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

type validatorFunc func(context.Context, json.RawMessage, []json.RawMessage) (runtimeir.Result, error)

func (f validatorFunc) Validate(ctx context.Context, raw json.RawMessage, inputs []json.RawMessage) (runtimeir.Result, error) {
	return f(ctx, raw, inputs)
}

type preflightFunc func(context.Context, []string) (services.DeviceSelectionResult, error)

func (f preflightFunc) Check(ctx context.Context, serials []string) (services.DeviceSelectionResult, error) {
	return f(ctx, serials)
}

type gateFunc func(context.Context) (func(), error)

func (f gateFunc) BeginAdmission(ctx context.Context) (func(), error) { return f(ctx) }

type repositoryStub struct {
	lookup   func(context.Context, string) (string, error)
	admit    func(context.Context, execution.Admission) (execution.AdmissionResult, error)
	snapshot func(context.Context, string) (execution.Snapshot, error)
}

func (r repositoryStub) LookupRequest(ctx context.Context, id string) (string, error) {
	return r.lookup(ctx, id)
}
func (r repositoryStub) Admit(ctx context.Context, a execution.Admission) (execution.AdmissionResult, error) {
	return r.admit(ctx, a)
}
func (r repositoryStub) SnapshotByID(ctx context.Context, id string) (execution.Snapshot, error) {
	return r.snapshot(ctx, id)
}

type unusedCheckpoints struct{ t *testing.T }

func (c unusedCheckpoints) BeginAttempt(context.Context, execution.BeginAttemptRequest) (execution.CheckpointResult, error) {
	c.t.Error("admission dispatched an attempt")
	return execution.CheckpointResult{}, xerror.ErrExecutionPersistence
}
func (c unusedCheckpoints) FinishAttempt(context.Context, execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
	c.t.Error("admission finished an attempt")
	return execution.CheckpointResult{}, xerror.ErrExecutionPersistence
}

func components(t *testing.T) (*runtimeir.Validator, runner.Dependencies, *atomic.Int32) {
	t.Helper()
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	calls := new(atomic.Int32)
	handler := func(context.Context, registry.Invocation) (map[string]any, error) {
		calls.Add(1)
		return nil, errors.New("admission invoked a handler")
	}
	handlers, err := registry.New(catalog, []registry.Registration{
		{Type: "flow.start", RuntimeHandler: "flow.start", Run: handler}, {Type: "flow.delay", RuntimeHandler: "flow.delay", Run: handler}, {Type: "flow.end", RuntimeHandler: "flow.end", Run: handler},
	})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := runtimeir.New(catalog, handlers, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := bindings.New(catalog)
	if err != nil {
		t.Fatal(err)
	}
	output, err := outputs.New(catalog)
	if err != nil {
		t.Fatal(err)
	}
	invoker, err := invocation.New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	return validator, runner.Dependencies{Bindings: resolver, Outputs: output, Invoker: invoker, Checkpoints: unusedCheckpoints{t}}, calls
}
func request(t *testing.T, count int) runadmission.Request {
	t.Helper()
	raw, err := os.ReadFile("../../execution/runner/testdata/workflow.json")
	if err != nil {
		t.Fatal(err)
	}
	req := runadmission.Request{RequestID: uuid.NewString(), RuntimeIR: raw, Devices: make([]runadmission.DeviceInput, count)}
	for i := range req.Devices {
		req.Devices[i] = runadmission.DeviceInput{ADBSerial: fmt.Sprintf("emulator-%d", 5554+2*i), Inputs: json.RawMessage(fmt.Sprintf(`{"duration":%d}`, i+1))}
	}
	return req
}
func service(t *testing.T, deps runadmission.Dependencies) *runadmission.Service {
	t.Helper()
	s, err := runadmission.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func unitDependencies(t *testing.T, trace *[]string) runadmission.Dependencies {
	t.Helper()
	validator, runnerDeps, _ := components(t)
	return runadmission.Dependencies{Runner: runnerDeps,
		Validator: validatorFunc(func(ctx context.Context, raw json.RawMessage, inputs []json.RawMessage) (runtimeir.Result, error) {
			*trace = append(*trace, "validate")
			return validator.Validate(ctx, raw, inputs)
		}),
		Preflight: preflightFunc(func(_ context.Context, serials []string) (services.DeviceSelectionResult, error) {
			*trace = append(*trace, "preflight")
			return services.DeviceSelectionResult{AcceptedSerials: slices.Clone(serials)}, nil
		}),
		Lifecycle: gateFunc(func(context.Context) (func(), error) {
			*trace = append(*trace, "lease")
			var once sync.Once
			return func() { once.Do(func() { *trace = append(*trace, "release") }) }, nil
		}),
		Repository: repositoryStub{
			lookup: func(context.Context, string) (string, error) {
				*trace = append(*trace, "lookup")
				return "", xerror.ErrExecutionNotFound
			},
			admit: func(_ context.Context, a execution.Admission) (execution.AdmissionResult, error) {
				*trace = append(*trace, "admit")
				return execution.AdmissionResult{Disposition: constants.AdmissionCreated, RunGroupID: a.RunGroupID}, nil
			},
			snapshot: func(context.Context, string) (execution.Snapshot, error) {
				t.Error("unexpected snapshot read")
				return execution.Snapshot{}, xerror.ErrExecutionPersistence
			},
		},
	}
}
func TestNewRequiresDependencies(t *testing.T) {
	var trace []string
	base := unitDependencies(t, &trace)
	for name, remove := range map[string]func(*runadmission.Dependencies){
		"validator": func(d *runadmission.Dependencies) { d.Validator = nil }, "preflight": func(d *runadmission.Dependencies) { d.Preflight = nil },
		"repository": func(d *runadmission.Dependencies) { d.Repository = nil }, "lifecycle": func(d *runadmission.Dependencies) { d.Lifecycle = nil },
		"bindings": func(d *runadmission.Dependencies) { d.Runner.Bindings = nil }, "outputs": func(d *runadmission.Dependencies) { d.Runner.Outputs = nil },
		"invoker": func(d *runadmission.Dependencies) { d.Runner.Invoker = nil }, "checkpoints": func(d *runadmission.Dependencies) { d.Runner.Checkpoints = nil },
	} {
		t.Run(name, func(t *testing.T) {
			deps := base
			remove(&deps)
			if _, err := runadmission.New(deps); !errors.Is(err, xerror.ErrRunAdmissionContract) {
				t.Fatalf("constructor: %v", err)
			}
		})
	}
}
func TestCreatedOrderingAndOwnedInputs(t *testing.T) {
	var trace []string
	deps := unitDependencies(t, &trace)
	req := request(t, 2)
	got, err := service(t, deps).Admit(t.Context(), req)
	if err != nil || got.Disposition != constants.AdmissionCreated || got.Work == nil || got.Snapshot != nil || got.Rejection != nil {
		t.Fatalf("created: %+v %v", got, err)
	}
	if !reflect.DeepEqual(trace, []string{"lookup", "validate", "preflight", "lease", "admit", "release"}) {
		t.Fatalf("order: %v", trace)
	}
	for i, device := range got.Work.Devices {
		if device.RunGroupID != got.RunGroupID || !execution.ValidID(device.DeviceExecutionID) || device.ADBSerial != req.Devices[i].ADBSerial || device.Control != nil || string(device.WorkflowInputs["duration"]) != fmt.Sprint(i+1) {
			t.Fatalf("pairing: %+v", device)
		}
	}
	clear(req.RuntimeIR)
	clear(req.Devices[0].Inputs)
	if string(got.Work.Devices[0].WorkflowInputs["duration"]) != "1" {
		t.Fatal("borrowed request bytes")
	}
}
func TestReplayPrecedesReplacementValidation(t *testing.T) {
	var trace []string
	deps := unitDependencies(t, &trace)
	req := runadmission.Request{RequestID: uuid.NewString(), RuntimeIR: json.RawMessage("invalid replacement")}
	id := uuid.NewString()
	repo := deps.Repository.(repositoryStub)
	repo.lookup = func(context.Context, string) (string, error) { trace = append(trace, "lookup"); return id, nil }
	repo.snapshot = func(context.Context, string) (execution.Snapshot, error) {
		trace = append(trace, "snapshot")
		return execution.Snapshot{RunGroup: execution.RunGroup{ID: id, RequestID: req.RequestID, Status: constants.ExecutionSucceeded}}, nil
	}
	deps.Repository = repo
	got, err := service(t, deps).Admit(t.Context(), req)
	if err != nil || got.Disposition != constants.AdmissionExisting || got.Work != nil || got.Rejection != nil || got.Snapshot.RunGroup.ID != id || !reflect.DeepEqual(trace, []string{"lookup", "snapshot"}) {
		t.Fatalf("replay: %+v %v %v", got, trace, err)
	}
}
func TestCommitOutcomes(t *testing.T) {
	for _, outcome := range []string{"existing", "busy", "rollback", "unknown", "invalid", "mismatched", "cancelled after commit"} {
		t.Run(outcome, func(t *testing.T) {
			var trace []string
			deps := unitDependencies(t, &trace)
			req := request(t, 1)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			id := uuid.NewString()
			repo := deps.Repository.(repositoryStub)
			repo.snapshot = func(context.Context, string) (execution.Snapshot, error) {
				if trace[len(trace)-1] != "release" {
					t.Error("snapshot holds admission lease")
				}
				return execution.Snapshot{RunGroup: execution.RunGroup{ID: id, RequestID: req.RequestID}}, nil
			}
			repo.admit = func(_ context.Context, a execution.Admission) (execution.AdmissionResult, error) {
				trace = append(trace, "admit")
				switch outcome {
				case "existing":
					return execution.AdmissionResult{Disposition: constants.AdmissionExisting, RunGroupID: id}, nil
				case "busy":
					return execution.AdmissionResult{Disposition: constants.AdmissionBusy}, nil
				case "rollback":
					return execution.AdmissionResult{}, xerror.ErrExecutionPersistence
				case "unknown":
					return execution.AdmissionResult{}, xerror.ErrExecutionCommitUnknown
				case "mismatched":
					return execution.AdmissionResult{Disposition: constants.AdmissionCreated, RunGroupID: id}, nil
				case "cancelled after commit":
					cancel()
					return execution.AdmissionResult{Disposition: constants.AdmissionCreated, RunGroupID: a.RunGroupID}, nil
				default:
					return execution.AdmissionResult{}, nil
				}
			}
			deps.Repository = repo
			got, err := service(t, deps).Admit(ctx, req)
			switch outcome {
			case "existing":
				if err != nil || got.Disposition != constants.AdmissionExisting || got.Work != nil || got.Snapshot == nil {
					t.Fatalf("existing: %+v %v", got, err)
				}
			case "busy":
				if err != nil || got.Disposition != constants.AdmissionBusy || got.Work != nil {
					t.Fatalf("busy: %+v %v", got, err)
				}
			case "cancelled after commit":
				if err != nil || got.Work == nil || ctx.Err() == nil {
					t.Fatalf("lost committed work: %+v %v", got, err)
				}
			default:
				want := xerror.ErrRunAdmissionContract
				if outcome == "rollback" {
					want = xerror.ErrExecutionPersistence
				}
				if outcome == "unknown" {
					want = xerror.ErrExecutionCommitUnknown
				}
				if !errors.Is(err, want) || got != (runadmission.Result{}) {
					t.Fatalf("fault: %+v %v", got, err)
				}
			}
			if trace[len(trace)-1] != "release" {
				t.Fatalf("lease leaked: %v", trace)
			}
		})
	}
}
func TestRequestGuardsBeforeValidation(t *testing.T) {
	for name, mutate := range map[string]func(*runadmission.Request){
		"id": func(r *runadmission.Request) { r.RequestID = "bad" }, "empty": func(r *runadmission.Request) { r.Devices = nil },
		"blank serials": func(r *runadmission.Request) { r.Devices = make([]runadmission.DeviceInput, 33) },
		"serial":        func(r *runadmission.Request) { r.Devices[0].ADBSerial = " invalid " },
		"duplicate":     func(r *runadmission.Request) { r.Devices = append(r.Devices, r.Devices[0]) },
		"ir bytes":      func(r *runadmission.Request) { r.RuntimeIR = make([]byte, constants.RuntimeIRMaxContentBytes+1) },
		"input bytes":   func(r *runadmission.Request) { r.Devices[0].Inputs = make([]byte, constants.RunRequestMaxContentBytes) },
	} {
		t.Run(name, func(t *testing.T) {
			var trace []string
			deps := unitDependencies(t, &trace)
			req := request(t, 1)
			mutate(&req)
			got, err := service(t, deps).Admit(t.Context(), req)
			want := xerror.ErrRunAdmissionInvalid
			if strings.HasSuffix(name, "bytes") {
				want = xerror.ErrRuntimeIRPayloadTooLarge
			}
			if !errors.Is(err, want) || got != (runadmission.Result{}) || slices.Contains(trace, "validate") {
				t.Fatalf("guard: %+v %v %v", got, trace, err)
			}
		})
	}
}
func TestCheckedContentGuardsAndWarnings(t *testing.T) {
	for _, change := range []string{"missing document", "missing inputs", "nil input", "failure code", "broken graph", "warnings"} {
		t.Run(change, func(t *testing.T) {
			var trace []string
			deps := unitDependencies(t, &trace)
			real := deps.Validator
			deps.Validator = validatorFunc(func(ctx context.Context, raw json.RawMessage, inputs []json.RawMessage) (runtimeir.Result, error) {
				out, err := real.Validate(ctx, raw, inputs)
				switch change {
				case "missing document":
					out.Document = nil
				case "missing inputs":
					out.DeviceInputs = nil
				case "nil input":
					out.DeviceInputs[0] = nil
				case "failure code":
					out.FailureCode = constants.ValidationFailed
				case "broken graph":
					out.Document.EntryNodeID = "missing"
				case "warnings":
					out.Validation.Status = validation.StatusValidWithWarnings
				}
				return out, err
			})
			got, err := service(t, deps).Admit(t.Context(), request(t, 1))
			if change == "warnings" {
				if err != nil || got.Work == nil {
					t.Fatalf("warning rejected: %+v %v", got, err)
				}
				return
			}
			if err == nil || got != (runadmission.Result{}) || slices.Contains(trace, "preflight") {
				t.Fatalf("trusted content accepted: %+v %v %v", got, trace, err)
			}
		})
	}
}
func TestPreflightGuards(t *testing.T) {
	for _, change := range []string{"reordered", "partial", "negative index", "out of range", "wrong serial", "duplicate issue", "unknown reason", "empty message"} {
		t.Run(change, func(t *testing.T) {
			var trace []string
			deps := unitDependencies(t, &trace)
			deps.Preflight = preflightFunc(func(_ context.Context, serials []string) (services.DeviceSelectionResult, error) {
				issue := services.DeviceSelectionIssue{Index: 0, ADBSerial: serials[0], Reason: adb.UnavailableReason{Code: constants.DeviceOffline, Message: "device offline"}}
				out := services.DeviceSelectionResult{Unavailable: []services.DeviceSelectionIssue{issue}}
				switch change {
				case "reordered":
					return services.DeviceSelectionResult{AcceptedSerials: []string{serials[1], serials[0]}}, nil
				case "partial":
					out.AcceptedSerials = serials
				case "negative index":
					out.Unavailable[0].Index = -1
				case "out of range":
					out.Unavailable[0].Index = len(serials)
				case "wrong serial":
					out.Unavailable[0].ADBSerial = "other"
				case "duplicate issue":
					out.Unavailable = append(out.Unavailable, issue)
				case "unknown reason":
					out.Unavailable[0].Reason.Code = "UNKNOWN"
				case "empty message":
					out.Unavailable[0].Reason.Message = ""
				}
				return out, nil
			})
			got, err := service(t, deps).Admit(t.Context(), request(t, 2))
			if !errors.Is(err, xerror.ErrRunAdmissionContract) || got != (runadmission.Result{}) || slices.Contains(trace, "lease") {
				t.Fatalf("trusted selection: %+v %v %v", got, trace, err)
			}
		})
	}
}
func TestCancellationAndOperationalErrors(t *testing.T) {
	for _, stage := range []string{"before", "validator", "preflight", "lease", "nil release", "lookup", "snapshot"} {
		t.Run(stage, func(t *testing.T) {
			var trace []string
			deps := unitDependencies(t, &trace)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req := request(t, 1)
			want := error(context.Canceled)
			switch stage {
			case "before":
				cancel()
			case "validator":
				deps.Validator = validatorFunc(func(context.Context, json.RawMessage, []json.RawMessage) (runtimeir.Result, error) {
					cancel()
					return runtimeir.Result{}, ctx.Err()
				})
			case "preflight":
				deps.Preflight = preflightFunc(func(context.Context, []string) (services.DeviceSelectionResult, error) {
					cancel()
					return services.DeviceSelectionResult{}, ctx.Err()
				})
			case "lease":
				deps.Lifecycle = gateFunc(func(context.Context) (func(), error) { return nil, xerror.ErrExecutionStopping })
				want = xerror.ErrExecutionStopping
			case "nil release":
				deps.Lifecycle = gateFunc(func(context.Context) (func(), error) { return nil, nil })
				want = xerror.ErrRunAdmissionContract
			case "lookup":
				repo := deps.Repository.(repositoryStub)
				repo.lookup = func(context.Context, string) (string, error) { return "", xerror.ErrExecutionPersistence }
				deps.Repository = repo
				want = xerror.ErrExecutionPersistence
			case "snapshot":
				repo := deps.Repository.(repositoryStub)
				repo.lookup = func(context.Context, string) (string, error) { return uuid.NewString(), nil }
				repo.snapshot = func(context.Context, string) (execution.Snapshot, error) {
					return execution.Snapshot{}, xerror.ErrExecutionPersistence
				}
				deps.Repository = repo
				want = xerror.ErrExecutionPersistence
			}
			got, err := service(t, deps).Admit(ctx, req)
			if !errors.Is(err, want) || got != (runadmission.Result{}) || slices.Contains(trace, "admit") {
				t.Fatalf("stop: %+v %v %v", got, trace, err)
			}
		})
	}
}

func TestIRByteBoundaryAndTotalBudget(t *testing.T) {
	for _, kind := range []string{"maximum IR", "maximum aggregate", "aggregate overflow"} {
		t.Run(kind, func(t *testing.T) {
			var trace []string
			deps := unitDependencies(t, &trace)
			req := request(t, 1)
			if kind == "maximum IR" {
				req.RuntimeIR = append(req.RuntimeIR, []byte(strings.Repeat(" ", constants.RuntimeIRMaxContentBytes-len(req.RuntimeIR)))...)
			} else {
				size := constants.RunRequestMaxContentBytes - len(req.RuntimeIR)
				if kind == "aggregate overflow" {
					size++
				}
				req.Devices[0].Inputs = json.RawMessage(strings.Repeat(" ", size))
			}
			out, err := service(t, deps).Admit(t.Context(), req)
			if kind == "aggregate overflow" {
				if !errors.Is(err, xerror.ErrRuntimeIRPayloadTooLarge) || slices.Contains(trace, "validate") {
					t.Fatalf("overflow: %+v %v %v", out, trace, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("boundary rejected by byte guard: %v", err)
			}
			if kind == "maximum IR" && out.Work == nil {
				t.Fatal("valid padded IR was rejected")
			}
			if kind == "maximum aggregate" && out.Rejection == nil {
				t.Fatal("expected input JSON rejection after passing exact byte boundary")
			}
		})
	}
}
func TestReplayRejectsCorruptRepositoryIdentity(t *testing.T) {
	for _, kind := range []string{"lookup ID", "snapshot group", "snapshot request"} {
		t.Run(kind, func(t *testing.T) {
			var trace []string
			deps := unitDependencies(t, &trace)
			req := request(t, 1)
			id := uuid.NewString()
			repo := deps.Repository.(repositoryStub)
			repo.lookup = func(context.Context, string) (string, error) {
				if kind == "lookup ID" {
					return "corrupt", nil
				}
				return id, nil
			}
			repo.snapshot = func(context.Context, string) (execution.Snapshot, error) {
				snapshot := execution.Snapshot{RunGroup: execution.RunGroup{ID: id, RequestID: req.RequestID}}
				if kind == "snapshot group" {
					snapshot.RunGroup.ID = uuid.NewString()
				} else {
					snapshot.RunGroup.RequestID = uuid.NewString()
				}
				return snapshot, nil
			}
			deps.Repository = repo
			out, err := service(t, deps).Admit(t.Context(), req)
			if !errors.Is(err, xerror.ErrRunAdmissionContract) || out != (runadmission.Result{}) {
				t.Fatalf("corrupt replay: %+v %v", out, err)
			}
		})
	}
}
func TestAdmissionBeforeConstructionFailsClosed(t *testing.T) {
	for _, s := range []*runadmission.Service{nil, new(runadmission.Service)} {
		out, err := s.Admit(t.Context(), request(t, 1))
		if !errors.Is(err, xerror.ErrRunAdmissionContract) || out != (runadmission.Result{}) {
			t.Fatalf("unconstructed service: %+v %v", out, err)
		}
	}
}
