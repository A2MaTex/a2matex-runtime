package services

import (
	"context"
	"fmt"
	"slices"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type DeviceSelectionIssue struct {
	Index     int
	ADBSerial string
	Reason    adb.UnavailableReason
}

type DeviceSelectionResult struct {
	AcceptedSerials []string
	Unavailable     []DeviceSelectionIssue
}

// DevicePreflight checks the selected devices immediately before admission.
type DevicePreflight struct {
	source DeviceInventorySource
}

func NewDevicePreflight(source DeviceInventorySource) *DevicePreflight {
	return &DevicePreflight{source: source}
}

// Check returns either the whole selection or its unavailable devices.
// Invalid input and failures to observe device state are returned as errors.
func (p *DevicePreflight) Check(ctx context.Context, serials []string) (DeviceSelectionResult, error) {
	if err := ctx.Err(); err != nil {
		return DeviceSelectionResult{}, xerror.WithOperation("check_device_preflight", err)
	}
	if err := validateSelection(serials); err != nil {
		return DeviceSelectionResult{}, xerror.WithOperation("validate_device_selection", err)
	}
	ctx, cancel := context.WithTimeout(ctx, constants.DevicePreflightTimeout)
	defer cancel()

	result, err := p.check(ctx, serials)
	if contextErr := ctx.Err(); contextErr != nil {
		return DeviceSelectionResult{}, xerror.WithOperation("check_device_preflight", contextErr)
	}
	return result, err
}

func (p *DevicePreflight) check(ctx context.Context, serials []string) (DeviceSelectionResult, error) {
	endpoints, err := p.source.Discover(ctx)
	if err != nil {
		return DeviceSelectionResult{}, xerror.WithOperation("discover_devices", err)
	}
	bySerial := make(map[string]adb.Endpoint, len(endpoints))
	for _, endpoint := range endpoints {
		bySerial[endpoint.ADBSerial] = endpoint
	}

	selected := make([]adb.Endpoint, 0, len(serials))
	for _, serial := range serials {
		if endpoint, found := bySerial[serial]; found {
			selected = append(selected, endpoint)
		}
	}
	devices, err := evaluateDeviceReadiness(ctx, p.source, selected, constants.DevicePreflightProbeLimit)
	if err != nil {
		return DeviceSelectionResult{}, err
	}
	var unavailable []DeviceSelectionIssue
	probeIndex := 0
	for index, serial := range serials {
		if _, found := bySerial[serial]; !found {
			unavailable = append(unavailable, DeviceSelectionIssue{Index: index, ADBSerial: serial, Reason: adb.UnavailableReason{
				Code: constants.DeviceNotFound, Message: "device is no longer connected",
			}})
			continue
		}
		device := devices[probeIndex]
		probeIndex++
		if device.ADBSerial != serial || (!device.Ready && device.Reason == nil) {
			return DeviceSelectionResult{}, xerror.WithOperation("validate_device_readiness_result", xerror.ErrADBReadiness)
		}
		if device.Reason != nil {
			unavailable = append(unavailable, DeviceSelectionIssue{Index: index, ADBSerial: serial, Reason: *device.Reason})
		}
	}
	if len(unavailable) != 0 {
		return DeviceSelectionResult{Unavailable: unavailable}, nil
	}
	return DeviceSelectionResult{AcceptedSerials: slices.Clone(serials)}, nil
}

func validateSelection(serials []string) error {
	if len(serials) == 0 {
		return fmt.Errorf("%w: expected at least one device", xerror.ErrInvalidDeviceSelection)
	}
	seen := make(map[string]struct{}, len(serials))
	for index, serial := range serials {
		if !adb.ValidSerial(serial) {
			return fmt.Errorf("%w: malformed serial at index %d", xerror.ErrInvalidDeviceSelection, index)
		}
		if _, duplicate := seen[serial]; duplicate {
			return fmt.Errorf("%w: duplicate serial at index %d", xerror.ErrInvalidDeviceSelection, index)
		}
		seen[serial] = struct{}{}
	}
	return nil
}
