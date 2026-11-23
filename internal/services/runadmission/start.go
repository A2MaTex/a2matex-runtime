package runadmission

import (
	"context"
	"errors"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Admitter interface {
	Admit(context.Context, Request) (Result, error)
}

type SnapshotReader interface {
	SnapshotByID(context.Context, string) (execution.Snapshot, error)
}

type ExecutionOwner interface {
	BeginStart(context.Context) (context.Context, func(), error)
	DispatchGroup(string, *runner.Runner, []runner.Request) error
	Fail(error)
}

type StartResult struct {
	Disposition constants.AdmissionDisposition
	Snapshot    *execution.Snapshot
	Rejection   *Rejection
}

type StartService struct {
	admission Admitter
	reader    SnapshotReader
	owner     ExecutionOwner
}

func NewStarter(admission Admitter, reader SnapshotReader, owner ExecutionOwner) (*StartService, error) {
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
	admitted, err := s.admission.Admit(admissionCtx, request)
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
		if validPreparedWork(admitted.RunGroupID, work) {
			if err := s.owner.DispatchGroup(admitted.RunGroupID, work.Runner, work.Devices); err != nil {
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

// Check the full committed-work contract before transferring any member.
func validPreparedWork(id string, work *PreparedRun) bool {
	if work == nil || work.Runner == nil || len(work.Devices) == 0 || !execution.ValidID(id) {
		return false
	}
	ids, serials := make(map[string]bool, len(work.Devices)), make(map[string]bool, len(work.Devices))
	for _, device := range work.Devices {
		view := device.Variables
		if device.RunGroupID != id || !execution.ValidID(device.DeviceExecutionID) || ids[device.DeviceExecutionID] ||
			!adb.ValidSerial(device.ADBSerial) || serials[device.ADBSerial] || device.WorkflowInputs == nil ||
			device.Control != nil || device.BeforeAttempt != nil || device.VariableStore != nil || view.RunGroupID != "" || view.DeviceExecutionID != "" || view.Lookup != nil {
			return false
		}
		ids[device.DeviceExecutionID], serials[device.ADBSerial] = true, true
	}
	return true
}
