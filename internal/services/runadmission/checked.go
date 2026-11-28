package runadmission

import (
	"context"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productworkflow"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// PrepareChecked is trusted composition: it borrows a J8-checked document once.
// The caller owns Loaded and the returned input/work resources until handoff.
func (s *Service) PrepareChecked(ctx context.Context, id, requestID string, devices []DeviceInput, loaded *productworkflow.Loaded) (execution.Admission, *PreparedRun, *Rejection, error) {
	if loaded == nil || loaded.Document == nil || !execution.ValidID(id) {
		return execution.Admission{}, nil, nil, xerror.ErrRunAdmissionContract
	}
	request := Request{RequestID: requestID, Devices: devices}
	serials, _, err := requestValues(request)
	if err != nil {
		return execution.Admission{}, nil, nil, err
	}
	worker, err := runner.New(ctx, loaded.Document, s.dependencies.Runner)
	if err != nil {
		return execution.Admission{}, nil, nil, err
	}
	selection, err := s.dependencies.Preflight.Check(ctx, serials)
	if err != nil {
		return execution.Admission{}, nil, nil, err
	}
	if err := checkedSelection(selection, serials); err != nil {
		return execution.Admission{}, nil, nil, err
	}
	if len(selection.Unavailable) > 0 {
		return execution.Admission{}, nil, &Rejection{FailureCode: selection.Unavailable[0].Reason.Code}, nil
	}
	inputs := loaded.TakeInputs()
	if len(inputs) != len(devices) {
		return execution.Admission{}, nil, nil, xerror.ErrRunAdmissionContract
	}
	admission, work, err := prepareIdentity(request, runtimeir.Result{Document: loaded.Document, DeviceInputs: inputs}, worker, id)
	if err != nil {
		for _, values := range inputs {
			for _, raw := range values {
				clear(raw)
			}
			clear(values)
		}
	}
	return admission, work, nil, err
}
