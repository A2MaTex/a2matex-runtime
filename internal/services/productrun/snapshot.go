package productrun

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type AuthorizationView struct {
	ExecutionID      string `json:"executionId"`
	State            string `json:"state"`
	RuntimeSessionID string `json:"runtimeSessionId"`
	LeaseFamilyID    string `json:"leaseFamilyId"`
	Sequence         int64  `json:"sequence"`
	RefreshAfter     string `json:"refreshAfter"`
	ExpiresAt        string `json:"expiresAt"`
}
type Device struct {
	ID            string               `json:"id"`
	ADBSerial     string               `json:"adbSerial"`
	Status        string               `json:"status"`
	CreatedAt     string               `json:"createdAt"`
	StartedAt     *string              `json:"startedAt"`
	FinishedAt    *string              `json:"finishedAt"`
	TerminalError *xerror.CommandError `json:"terminalError"`
}
type Snapshot struct {
	Cursor        string                          `json:"cursor"`
	RunGroup      execution.RunGroup              `json:"runGroup"`
	Devices       []Device                        `json:"deviceExecutions"`
	Product       packagevalidation.PublicPackage `json:"product"`
	Authorization AuthorizationView               `json:"authorization"`
}

func publicStamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func SafeError(code string) *xerror.CommandError {
	message := constants.ProductFailureMessage
	switch code {
	case constants.DeviceNotFound:
		message = "device is no longer connected"
	case constants.DeviceUnauthorized:
		message = "device authorization is required"
	case constants.DeviceOffline:
		message = "device is offline"
	case constants.DeviceNotReady:
		message = "device is not ready"
	case constants.DeviceDisconnected:
		message = "device disconnected"
	case constants.ExecutionInterruptedCode:
		message = constants.ExecutionInterruptedMessage
	default:
		code = constants.CommandFailed
	}
	return &xerror.CommandError{Code: code, Message: message, Retryable: false}
}
func (s *Service) project(ctx context.Context, raw execution.Snapshot) (Snapshot, error) {
	if raw.Product == nil {
		return Snapshot{}, xerror.ErrExecutionNotFound
	}
	if !execution.ValidProductProvenance(raw.Product) {
		return Snapshot{}, xerror.ErrRecoveryIntegrity
	}
	p := raw.Product
	result := Snapshot{Cursor: raw.Cursor, RunGroup: raw.RunGroup, Product: p.Package, Devices: make([]Device, len(raw.DeviceExecutions)), Authorization: AuthorizationView{ExecutionID: raw.RunGroup.ID, State: string(constants.ProductAuthorizationEnded), RuntimeSessionID: p.RuntimeSessionID, LeaseFamilyID: p.LeaseFamilyID, Sequence: p.Sequence, RefreshAfter: publicStamp(p.RefreshAfter), ExpiresAt: publicStamp(p.ExpiresAt)}}
	for i, d := range raw.DeviceExecutions {
		result.Devices[i] = Device{ID: d.ID, ADBSerial: d.ADBSerial, Status: d.Status, CreatedAt: d.CreatedAt, StartedAt: d.StartedAt, FinishedAt: d.FinishedAt}
		if len(d.TerminalError) > 0 {
			var command xerror.CommandError
			_ = json.Unmarshal(d.TerminalError, &command)
			result.Devices[i].TerminalError = SafeError(command.Code)
		}
	}
	if raw.RunGroup.Status == constants.ExecutionRunning || raw.RunGroup.Status == constants.ExecutionPaused {
		auth, err := s.owner.ProductAuthorization(ctx, raw.RunGroup.ID)
		if errors.Is(err, xerror.ErrExecutionNotFound) {
			again, readErr := s.repository.SnapshotByID(ctx, raw.RunGroup.ID)
			if readErr != nil {
				return Snapshot{}, readErr
			}
			if again.RunGroup.Status != constants.ExecutionRunning && again.RunGroup.Status != constants.ExecutionPaused {
				return s.project(ctx, again)
			}
			return Snapshot{}, xerror.ErrRecoveryIntegrity
		}
		if err != nil {
			return Snapshot{}, err
		}
		if auth.ExecutionID != raw.RunGroup.ID || auth.RuntimeSessionID != p.RuntimeSessionID || auth.LeaseFamilyID != p.LeaseFamilyID {
			return Snapshot{}, xerror.ErrRecoveryIntegrity
		}
		if auth.Phase == constants.ProductAuthorizationExpiring {
			return Snapshot{}, xerror.ErrProductRunUnavailable
		}
		result.Authorization.State = string(auth.Phase)
	}
	return result, nil
}

// Action classifies before control submission; only safe snapshots leave this facade.
func (s *Service) Action(ctx context.Context, id, action string) (Snapshot, constants.GroupControlDisposition, error) {
	raw, err := s.repository.SnapshotByID(ctx, id)
	if err != nil {
		return Snapshot{}, "", err
	}
	if raw.Product == nil {
		return Snapshot{}, "", xerror.ErrExecutionNotFound
	}
	var disposition constants.GroupControlDisposition
	switch action {
	case constants.RunActionPause:
		disposition, err = s.owner.Pause(ctx, id)
	case constants.RunActionResume:
		disposition, err = s.owner.Resume(ctx, id)
	case constants.RunActionCancel:
		if raw.RunGroup.Status == constants.ExecutionCancelled {
			snap, err := s.project(ctx, raw)
			return snap, constants.GroupControlAlreadyTarget, err
		}
		disposition, err = s.owner.Cancel(ctx, id)
	default:
		return Snapshot{}, "", xerror.ErrProductSessionInvalid
	}
	if err != nil && !errors.Is(err, xerror.ErrExecutionControlState) && !errors.Is(err, xerror.ErrProductAuthorizationExpired) {
		return Snapshot{}, "", err
	}
	snapshot, readErr := s.ByID(ctx, id)
	if readErr != nil {
		return Snapshot{}, disposition, readErr
	}
	return snapshot, disposition, err
}
