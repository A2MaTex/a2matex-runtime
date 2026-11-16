package adb

import (
	"bytes"
	"context"
	"fmt"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/processrunner"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Readiness struct {
	ADBSerial string
	Ready     bool
	Reason    *UnavailableReason
}

type UnavailableReason struct {
	Code    string
	Message string
}

// ProbeReadiness checks one endpoint from discovery. A nil error indicates
// an observed device state. An error indicates the observation could not be made.
func (r *Resolver) ProbeReadiness(ctx context.Context, endpoint Endpoint) (Readiness, error) {
	if err := ctx.Err(); err != nil {
		return Readiness{}, xerror.WithOperation("check_device_readiness", err)
	}
	if !ValidSerial(endpoint.ADBSerial) {
		return Readiness{}, xerror.WithOperation("validate_device_endpoint", xerror.ErrADBInvalidEndpoint)
	}
	switch endpoint.State {
	case Offline, Unauthorized, OtherUnavailable:
		return unavailableForState(endpoint), nil
	case Connected:
	default:
		return Readiness{}, xerror.WithOperation("validate_device_endpoint", xerror.ErrADBInvalidEndpoint)
	}

	probeCtx, cancel := context.WithTimeout(ctx, constants.ADBReadinessTimeout)
	defer cancel()

	result, err := r.probeBoot(probeCtx, endpoint)
	if contextErr := ctx.Err(); contextErr != nil {
		return Readiness{}, xerror.WithOperation("check_device_readiness", contextErr)
	}
	if probeCtx.Err() != nil {
		return unavailableForState(endpoint), nil
	}
	return result, err
}

func (r *Resolver) probeBoot(ctx context.Context, endpoint Endpoint) (Readiness, error) {
	toolchain, err := r.Resolve(ctx)
	if err != nil {
		return Readiness{}, fmt.Errorf("%w: %w", xerror.ErrADBReadiness, err)
	}

	args := []string{"-s", endpoint.ADBSerial, "shell", "-T", "getprop", "sys.boot_completed"}
	result := r.run(ctx, toolchain.Executable, args, constants.ADBReadinessTimeout)
	if err := ctx.Err(); err != nil {
		return Readiness{}, xerror.WithOperation("read_boot_status", err)
	}
	switch result.Outcome {
	case processrunner.Succeeded:
		if result.ExitCode == nil || *result.ExitCode != 0 {
			return Readiness{}, xerror.WithOperation("read_boot_status", xerror.ErrADBReadiness)
		}
		if !result.StdoutTruncated && bytes.Equal(bytes.Trim(result.Stdout, " \t\r\n"), []byte("1")) {
			return Readiness{ADBSerial: endpoint.ADBSerial, Ready: true}, nil
		}
		return unavailableForState(endpoint), nil
	case processrunner.TimedOut:
		return unavailableForState(endpoint), nil
	case processrunner.NonzeroExit:
		return r.recheckTransport(ctx, endpoint.ADBSerial)
	default:
		return Readiness{}, xerror.WithOperation("read_boot_status", fmt.Errorf("%w: command did not succeed (%s)", xerror.ErrADBReadiness, result.Outcome))
	}
}

func (r *Resolver) recheckTransport(ctx context.Context, serial string) (Readiness, error) {
	endpoints, err := r.Discover(ctx)
	if err != nil {
		return Readiness{}, err
	}
	for _, endpoint := range endpoints {
		if endpoint.ADBSerial == serial {
			return unavailableForState(endpoint), nil
		}
	}
	return unavailable(serial, constants.DeviceNotFound, "device is no longer connected"), nil
}

func unavailableForState(endpoint Endpoint) Readiness {
	switch endpoint.State {
	case Offline:
		return unavailable(endpoint.ADBSerial, constants.DeviceOffline, "device is offline")
	case Unauthorized:
		return unavailable(endpoint.ADBSerial, constants.DeviceUnauthorized, "device is unauthorized")
	default:
		return unavailable(endpoint.ADBSerial, constants.DeviceNotReady, "device is not ready")
	}
}

func unavailable(serial, code, message string) Readiness {
	return Readiness{ADBSerial: serial, Reason: &UnavailableReason{Code: code, Message: message}}
}
