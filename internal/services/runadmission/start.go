package runadmission

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/variables"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type SingleDeviceAdmitter interface {
	AdmitSingleDevice(context.Context, Request) (Result, error)
}

type SnapshotReader interface {
	SnapshotByID(context.Context, string) (execution.Snapshot, error)
}

type ExecutionOwner interface {
	BeginStart(context.Context) (context.Context, func(), error)
	Dispatch(*runner.Runner, runner.Request, func(context.Context) error) error
	Fail(error)
}

type StartResult struct {
	Disposition constants.AdmissionDisposition
	Snapshot    *execution.Snapshot
	Rejection   *Rejection
}

type StartService struct {
	admission SingleDeviceAdmitter
	reader    SnapshotReader
	owner     ExecutionOwner
}

func NewStarter(admission SingleDeviceAdmitter, reader SnapshotReader, owner ExecutionOwner) (*StartService, error) {
	if admission == nil || reader == nil || owner == nil {
		return nil, xerror.WithOperation("configure_run_start", xerror.ErrRunAdmissionContract)
	}
	return &StartService{admission: admission, reader: reader, owner: owner}, nil
}

func (s *StartService) Start(ctx context.Context, request Request) (StartResult, error) {
	admissionCtx, release, err := s.owner.BeginStart(ctx)
	if err != nil {
		return StartResult{}, xerror.WithOperation("begin_run_start", err)
	}
	if admissionCtx == nil || release == nil {
		err = xerror.WithOperation("begin_run_start", xerror.ErrRunAdmissionContract)
		s.owner.Fail(err)
		if release != nil {
			release()
		}
		return StartResult{}, err
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	admitted, err := s.admission.AdmitSingleDevice(admissionCtx, request)
	if err != nil {
		if errors.Is(err, xerror.ErrExecutionCommitUnknown) || errors.Is(err, xerror.ErrRunAdmissionContract) {
			s.owner.Fail(err)
		}
		return StartResult{}, err
	}
	if admitted.Rejection != nil {
		return StartResult{Rejection: admitted.Rejection}, nil
	}
	switch admitted.Disposition {
	case constants.AdmissionBusy:
		return StartResult{Disposition: admitted.Disposition}, nil
	case constants.AdmissionExisting:
		if admitted.Snapshot != nil && admitted.Work == nil {
			return StartResult{Disposition: admitted.Disposition, Snapshot: admitted.Snapshot}, nil
		}
	case constants.AdmissionCreated:
		work := admitted.Work
		if work != nil && work.Runner != nil && len(work.Devices) == constants.MaxStartDevices &&
			execution.ValidID(admitted.RunGroupID) && work.Devices[0].RunGroupID == admitted.RunGroupID &&
			execution.ValidID(work.Devices[0].DeviceExecutionID) && work.Devices[0].WorkflowInputs != nil &&
			work.Devices[0].ADBSerial != "" && work.Devices[0].Control == nil && work.Devices[0].VariableStore == nil &&
			work.Devices[0].Variables.RunGroupID == "" && work.Devices[0].Variables.DeviceExecutionID == "" && work.Devices[0].Variables.Lookup == nil {
			device := work.Devices[0]
			store, err := variables.New(admitted.RunGroupID, []string{device.DeviceExecutionID})
			if err != nil {
				err = xerror.WithOperation("create_run_variables", err)
				s.owner.Fail(err)
				return StartResult{}, err
			}
			device.VariableStore = store
			deviceID := device.DeviceExecutionID
			device.Variables = bindings.VariableView{RunGroupID: admitted.RunGroupID, DeviceExecutionID: device.DeviceExecutionID,
				Lookup: func(ctx context.Context, scope constants.VariableScope, name string) (json.RawMessage, bool, error) {
					return store.Get(ctx, deviceID, scope, name)
				}}
			if err := s.owner.Dispatch(work.Runner, device, store.Close); err != nil {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(admissionCtx), constants.CheckpointTimeout)
				cleanupErr := store.Close(cleanupCtx)
				cancel()
				if cleanupErr != nil {
					err = errors.Join(err, cleanupErr)
				}
				err = xerror.WithOperation("handoff_admitted_run", err)
				s.owner.Fail(err)
				return StartResult{}, err
			}
			release()
			release = nil
			snapshot, err := s.reader.SnapshotByID(ctx, admitted.RunGroupID)
			if err != nil {
				return StartResult{}, xerror.WithOperation("read_started_run_snapshot", err)
			}
			if snapshot.RunGroup.ID != admitted.RunGroupID || snapshot.RunGroup.RequestID != request.RequestID {
				return StartResult{}, xerror.WithOperation("read_started_run_snapshot", xerror.ErrRunAdmissionContract)
			}
			return StartResult{Disposition: admitted.Disposition, Snapshot: &snapshot}, nil
		}
	}
	err = xerror.WithOperation("handoff_admitted_run", xerror.ErrRunAdmissionContract)
	s.owner.Fail(err)
	return StartResult{}, err
}
