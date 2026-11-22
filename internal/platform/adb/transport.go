package adb

import (
	"context"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// classifyTransportFailure observes a failed command without repeating its action.
func (d *Dispatcher) classifyTransportFailure(ctx context.Context, serial string, original error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	observation, cancel := context.WithTimeout(ctx, constants.ADBDiscoveryTimeout)
	defer cancel()
	endpoints, err := d.resolver.Discover(observation)
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if err != nil || observation.Err() != nil {
		// A failed observation cannot prove device loss or make the action retryable.
		return original
	}
	for _, endpoint := range endpoints {
		if endpoint.ADBSerial != serial {
			continue
		}
		if endpoint.State == Connected {
			return original
		}
		reason := unavailableForState(endpoint).Reason
		return xerror.CommandError{Code: reason.Code, Message: reason.Message}
	}
	return xerror.CommandError{Code: constants.DeviceDisconnected, Message: "device disconnected during command execution"}
}
