package runadmission

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

func requestValues(request Request) ([]string, []json.RawMessage, error) {
	const operation = "check_run_admission_request"
	if len(request.Devices) == 0 {
		return nil, nil, xerror.WithOperation(operation, xerror.ErrRunAdmissionInvalid)
	}
	if len(request.RuntimeIR) > constants.RuntimeIRMaxContentBytes {
		return nil, nil, xerror.WithOperation(operation, xerror.ErrRuntimeIRPayloadTooLarge)
	}
	remaining := constants.RunRequestMaxContentBytes - len(request.RuntimeIR)
	serials := make([]string, len(request.Devices))
	inputs := make([]json.RawMessage, len(request.Devices))
	seen := make(map[string]bool, len(request.Devices))
	for i, device := range request.Devices {
		if !adb.ValidSerial(device.ADBSerial) || seen[device.ADBSerial] {
			return nil, nil, xerror.WithOperation(operation, xerror.ErrRunAdmissionInvalid)
		}
		if len(device.Inputs) > remaining {
			return nil, nil, xerror.WithOperation(operation, xerror.ErrRuntimeIRPayloadTooLarge)
		}
		remaining -= len(device.Inputs)
		serials[i] = device.ADBSerial
		inputs[i] = device.Inputs
		seen[device.ADBSerial] = true
	}
	return serials, inputs, nil
}

func checkedContent(result runtimeir.Result, count int) error {
	invalid := result.Validation.Status == validation.StatusInvalid
	if invalid {
		if (result.FailureCode == constants.ValidationFailed || result.FailureCode == constants.RuntimeIncompatible) &&
			result.Document == nil && result.DeviceInputs == nil && result.RawIR == nil && len(result.Validation.Errors) > 0 {
			return nil
		}
	} else if (result.Validation.Status == validation.StatusValid || result.Validation.Status == validation.StatusValidWithWarnings) &&
		result.FailureCode == "" && len(result.Validation.Errors) == 0 && result.Document != nil && len(result.DeviceInputs) == count {
		for _, inputs := range result.DeviceInputs {
			if inputs == nil {
				return xerror.WithOperation("check_admitted_content", xerror.ErrRunAdmissionContract)
			}
		}
		return nil
	}
	return xerror.WithOperation("check_admitted_content", xerror.ErrRunAdmissionContract)
}

func checkedSelection(selection services.DeviceSelectionResult, serials []string) error {
	fail := func() error { return xerror.WithOperation("check_admitted_devices", xerror.ErrRunAdmissionContract) }
	if len(selection.Unavailable) == 0 {
		if !slices.Equal(selection.AcceptedSerials, serials) {
			return fail()
		}
		return nil
	}
	if len(selection.AcceptedSerials) != 0 {
		return fail()
	}
	previous := -1
	for _, issue := range selection.Unavailable {
		if issue.Index <= previous || issue.Index >= len(serials) || issue.ADBSerial != serials[issue.Index] {
			return fail()
		}
		switch issue.Reason.Code {
		case constants.DeviceNotFound, constants.DeviceUnauthorized, constants.DeviceOffline, constants.DeviceNotReady:
		default:
			return fail()
		}
		if !utf8.ValidString(issue.Reason.Message) || strings.TrimSpace(issue.Reason.Message) == "" ||
			utf8.RuneCountInString(issue.Reason.Message) > constants.MaxRuntimeErrorMessageChars {
			return fail()
		}
		previous = issue.Index
	}
	return nil
}

func prepare(request Request, checked runtimeir.Result, worker *runner.Runner) (execution.Admission, *PreparedRun, error) {
	groupID, err := uuid.NewRandom()
	if err != nil {
		return execution.Admission{}, nil, xerror.WithOperation("prepare_run_admission", err)
	}
	admission := execution.Admission{RequestID: request.RequestID, RunGroupID: groupID.String(), Devices: make([]execution.AdmissionDevice, len(request.Devices))}
	work := &PreparedRun{Runner: worker, Devices: make([]runner.Request, len(request.Devices))}
	for i, device := range request.Devices {
		deviceID, err := uuid.NewRandom()
		if err != nil {
			return execution.Admission{}, nil, xerror.WithOperation("prepare_run_admission", err)
		}
		admission.Devices[i] = execution.AdmissionDevice{DeviceExecutionID: deviceID.String(), ADBSerial: device.ADBSerial, EntryNodeID: checked.Document.EntryNodeID}
		work.Devices[i] = runner.Request{RunGroupID: admission.RunGroupID, DeviceExecutionID: deviceID.String(), ADBSerial: device.ADBSerial, WorkflowInputs: ir.Object(checked.DeviceInputs[i])}
	}
	admission.Now = time.Now().UTC()
	return admission, work, nil
}

func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}
