package repositories

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func (s *ExecutionRepository) LookupRequest(ctx context.Context, requestID string) (string, error) {
	if !execution.ValidID(requestID) {
		return "", xerror.ErrExecutionInvalidInput
	}
	var id string
	err := s.withReadTx(ctx, func(tx Tx) error {
		var err error
		id, err = lookupRequest(ctx, tx, requestID)
		return err
	})
	return id, err
}

func lookupRequest(ctx context.Context, tx Tx, requestID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM run_groups WHERE request_id = ?", requestID).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", xerror.ErrExecutionNotFound
	case err != nil:
		return "", storageError(ctx, "look_up_admitted_request", err)
	default:
		return id, nil
	}
}

func (s *ExecutionRepository) Admit(ctx context.Context, admission execution.Admission) (execution.AdmissionResult, error) {
	if !execution.ValidID(admission.RequestID) {
		return execution.AdmissionResult{}, xerror.ErrExecutionInvalidInput
	}
	validNew := execution.ValidNewAdmission(admission)
	var result execution.AdmissionResult
	err := s.WithWriteTx(ctx, func(tx Tx) error {
		id, err := lookupRequest(ctx, tx, admission.RequestID)
		if err == nil {
			kind, err := executionKind(ctx, tx, id)
			if err != nil {
				return err
			}
			if (admission.Product != nil) != (kind == constants.ExecutionKindProduct) {
				return xerror.ErrProductRunConflict
			}
			result = execution.AdmissionResult{Disposition: constants.AdmissionExisting, RunGroupID: id}
			return nil
		}
		if !errors.Is(err, xerror.ErrExecutionNotFound) {
			return err
		}
		if !validNew {
			return xerror.ErrExecutionInvalidInput
		}
		var busy bool
		const active = "SELECT EXISTS(SELECT 1 FROM run_groups WHERE status IN ('RUNNING','PAUSED'))"
		if err := tx.QueryRowContext(ctx, active).Scan(&busy); err != nil {
			return storageError(ctx, "check_active_run", err)
		}
		if busy {
			result = execution.AdmissionResult{Disposition: constants.AdmissionBusy}
			return nil
		}
		if err := insertAdmission(ctx, tx, admission); err != nil {
			return err
		}
		result = execution.AdmissionResult{Disposition: constants.AdmissionCreated, RunGroupID: admission.RunGroupID}
		return nil
	})
	if err != nil {
		return execution.AdmissionResult{}, err
	}
	return result, nil
}

func insertAdmission(ctx context.Context, tx Tx, admission execution.Admission) error {
	now := admission.Now.UnixMilli()
	kind := constants.ExecutionKindDevelopment
	if admission.Product != nil {
		kind = constants.ExecutionKindProduct
	}
	const group = "INSERT INTO run_groups(id,request_id,status,created_at,execution_kind) VALUES (?,?,'RUNNING',?,?)"
	if _, err := tx.ExecContext(ctx, group, admission.RunGroupID, admission.RequestID, now, kind); err != nil {
		return storageError(ctx, "insert_run_group", err)
	}
	if admission.Product != nil {
		p := admission.Product
		const query = `INSERT INTO product_run_provenance VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
		if _, err := tx.ExecContext(ctx, query, admission.RunGroupID, p.Package.ProductID, p.Package.ReleaseID, p.Package.Version, p.Package.PackageHash, p.RuntimeIRHash, p.RootHash, p.RuntimeSessionID, p.LeaseFamilyID, p.Sequence, p.RefreshAfter.UnixMilli(), p.ExpiresAt.UnixMilli()); err != nil {
			return storageError(ctx, "insert_product_provenance", err)
		}
	}
	const devices = "INSERT INTO device_executions(id,run_group_id,adb_serial,admission_ordinal,status,next_node_id,created_at) VALUES "
	// Bound SQL parameters without limiting the admitted group or committing a partial selection.
	for offset := 0; offset < len(admission.Devices); {
		end := offset + min(constants.DeviceInsertBatchSize, len(admission.Devices)-offset)
		deviceRows, deviceArgs := make([]string, 0, end-offset), make([]any, 0, (end-offset)*6)
		for index, device := range admission.Devices[offset:end] {
			deviceRows = append(deviceRows, "(?,?,?,?,'QUEUED',?,?)")
			deviceArgs = append(deviceArgs, device.DeviceExecutionID, admission.RunGroupID, device.ADBSerial, offset+index, device.EntryNodeID, now)
		}
		if _, err := tx.ExecContext(ctx, devices+strings.Join(deviceRows, ","), deviceArgs...); err != nil {
			return storageError(ctx, "insert_selected_devices", err)
		}
		offset = end
	}
	return appendAdmissionEvents(ctx, tx, admission)
}

func appendAdmissionEvents(ctx context.Context, tx Tx, admission execution.Admission) error {
	events := make([]execution.PendingEvent, 0, len(admission.Devices)+1)
	events = append(events, execution.PendingEvent{Type: constants.EventRunGroupStateChanged,
		RunGroupID: admission.RunGroupID, OccurredAt: admission.Now, Payload: json.RawMessage(`{"status":"RUNNING"}`)})
	for _, device := range admission.Devices {
		payload, err := json.Marshal(struct {
			Status     string `json:"status"`
			NextNodeID string `json:"nextNodeId"`
		}{"QUEUED", device.EntryNodeID})
		if err != nil {
			return storageError(ctx, "encode_device_admission_event", err)
		}
		events = append(events, execution.PendingEvent{Type: constants.EventDeviceStateChanged,
			RunGroupID: admission.RunGroupID, DeviceExecutionID: device.DeviceExecutionID,
			OccurredAt: admission.Now, Payload: payload})
	}
	_, err := execution.AppendEventBatches(ctx, tx, events)
	return err
}
