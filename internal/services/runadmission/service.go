package runadmission

import (
	"context"
	"errors"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Service struct{ dependencies Dependencies }

func New(dependencies Dependencies) (*Service, error) {
	if dependencies.Validator == nil || dependencies.Preflight == nil || dependencies.Repository == nil || dependencies.Lifecycle == nil ||
		dependencies.Runner.Bindings == nil || dependencies.Runner.Invoker == nil || dependencies.Runner.Outputs == nil || dependencies.Runner.Checkpoints == nil {
		return nil, xerror.WithOperation("configure_run_admission", xerror.ErrRunAdmissionContract)
	}
	return &Service{dependencies: dependencies}, nil
}

// Admit returns owned work after confirmed commit, even if the request is then
// cancelled. Its trusted caller must hand that work to owner-bound scheduling.
func (s *Service) Admit(ctx context.Context, request Request) (Result, error) {
	if err := contextError(ctx); err != nil {
		return Result{}, xerror.WithOperation("check_run_admission_context", err)
	}
	if s == nil || s.dependencies.Repository == nil {
		return Result{}, xerror.WithOperation("configure_run_admission", xerror.ErrRunAdmissionContract)
	}
	if !execution.ValidID(request.RequestID) {
		return Result{}, xerror.WithOperation("check_run_admission_request", xerror.ErrRunAdmissionInvalid)
	}
	id, err := s.dependencies.Repository.LookupRequest(ctx, request.RequestID)
	if err == nil {
		return s.replay(ctx, request.RequestID, id)
	}
	if !errors.Is(err, xerror.ErrExecutionNotFound) {
		return Result{}, xerror.WithOperation("look_up_run_admission", err)
	}
	serials, inputs, err := requestValues(request)
	if err != nil {
		return Result{}, err
	}
	checked, err := s.dependencies.Validator.Validate(ctx, request.RuntimeIR, inputs)
	if err != nil {
		return Result{}, xerror.WithOperation("validate_run_admission", err)
	}
	if err := contextError(ctx); err != nil {
		return Result{}, xerror.WithOperation("validate_run_admission", err)
	}
	if err := checkedContent(checked, len(serials)); err != nil {
		return Result{}, err
	}
	if checked.Validation.Status == validation.StatusInvalid {
		return Result{Rejection: &Rejection{FailureCode: checked.FailureCode, Validation: &checked.Validation}}, nil
	}
	worker, err := runner.New(ctx, checked.Document, s.dependencies.Runner)
	if err != nil {
		return Result{}, xerror.WithOperation("prepare_admitted_runner", err)
	}
	selection, err := s.dependencies.Preflight.Check(ctx, serials)
	if err != nil {
		return Result{}, xerror.WithOperation("preflight_run_admission", err)
	}
	if err := contextError(ctx); err != nil {
		return Result{}, xerror.WithOperation("preflight_run_admission", err)
	}
	if err := checkedSelection(selection, serials); err != nil {
		return Result{}, err
	}
	if len(selection.Unavailable) != 0 {
		return Result{Rejection: &Rejection{FailureCode: selection.Unavailable[0].Reason.Code, Unavailable: selection.Unavailable}}, nil
	}
	admission, work, err := prepare(request, checked, worker)
	if err != nil {
		return Result{}, err
	}
	if err := contextError(ctx); err != nil {
		return Result{}, xerror.WithOperation("prepare_run_admission", err)
	}
	release, err := s.dependencies.Lifecycle.BeginAdmission(ctx)
	if err != nil {
		return Result{}, xerror.WithOperation("fence_run_admission", err)
	}
	if release == nil {
		return Result{}, xerror.WithOperation("fence_run_admission", xerror.ErrRunAdmissionContract)
	}
	defer release()
	admitted, err := s.dependencies.Repository.Admit(ctx, admission)
	if err != nil {
		return Result{}, xerror.WithOperation("commit_run_admission", err)
	}
	switch admitted.Disposition {
	case constants.AdmissionCreated:
		if admitted.RunGroupID != admission.RunGroupID {
			break
		}
		// No cancellable snapshot read or final context check may discard committed work.
		return Result{Disposition: constants.AdmissionCreated, RunGroupID: admission.RunGroupID, Work: work}, nil
	case constants.AdmissionExisting:
		release()
		return s.replay(ctx, request.RequestID, admitted.RunGroupID)
	case constants.AdmissionBusy:
		if admitted.RunGroupID != "" {
			break
		}
		return Result{Disposition: constants.AdmissionBusy}, nil
	}
	return Result{}, xerror.WithOperation("commit_run_admission", xerror.ErrRunAdmissionContract)
}

func (s *Service) replay(ctx context.Context, requestID, id string) (Result, error) {
	if !execution.ValidID(id) {
		return Result{}, xerror.WithOperation("read_admitted_run", xerror.ErrRunAdmissionContract)
	}
	snapshot, err := s.dependencies.Repository.SnapshotByID(ctx, id)
	if err != nil {
		return Result{}, xerror.WithOperation("read_admitted_run", err)
	}
	if snapshot.RunGroup.ID != id || snapshot.RunGroup.RequestID != requestID {
		return Result{}, xerror.WithOperation("read_admitted_run", xerror.ErrRunAdmissionContract)
	}
	return Result{Disposition: constants.AdmissionExisting, RunGroupID: id, Snapshot: &snapshot}, nil
}
