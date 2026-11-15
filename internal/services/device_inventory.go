package services

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

type DeviceInventorySource interface {
	Discover(context.Context) ([]adb.Endpoint, error)
	ProbeReadiness(context.Context, adb.Endpoint) (adb.Readiness, error)
}

type inventoryRefresh struct {
	done    chan struct{}
	devices []adb.Readiness
	err     error
}

// DeviceInventory retains the last successful device refresh for this process.
type DeviceInventory struct {
	source DeviceInventorySource
	mu     sync.RWMutex
	// Published snapshots are immutable. Callers receive their own copies.
	devices []adb.Readiness
	pending *inventoryRefresh
}

func NewDeviceInventory(source DeviceInventorySource) *DeviceInventory {
	return &DeviceInventory{source: source}
}

func (s *DeviceInventory) Snapshot() []adb.Readiness {
	s.mu.RLock()
	devices := s.devices
	s.mu.RUnlock()
	return copyInventory(devices)
}

func (s *DeviceInventory) Refresh(ctx context.Context) ([]adb.Readiness, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if pending := s.pending; pending != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-pending.done:
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if pending.err != nil {
				return nil, pending.err
			}
			return copyInventory(pending.devices), nil
		}
	}

	refreshCtx, cancel := context.WithTimeout(ctx, constants.DeviceInventoryRefreshTimeout)
	defer cancel()
	pending := &inventoryRefresh{done: make(chan struct{})}
	s.pending = pending
	s.mu.Unlock()

	devices, err := s.collect(refreshCtx)

	s.mu.Lock()
	if contextErr := refreshCtx.Err(); contextErr != nil {
		err = contextErr
	}
	if err == nil {
		s.devices = devices
		pending.devices = devices
	}
	pending.err = err
	s.pending = nil
	close(pending.done)
	s.mu.Unlock()

	if err != nil {
		return nil, err
	}
	return copyInventory(devices), nil
}

func (s *DeviceInventory) collect(ctx context.Context) ([]adb.Readiness, error) {
	endpoints, err := s.source.Discover(ctx)
	if err != nil {
		return nil, err
	}

	devices, err := evaluateDeviceReadiness(ctx, s.source, endpoints, constants.DeviceInventoryProbeLimit)
	if err != nil {
		return nil, err
	}

	devices = slices.DeleteFunc(devices, func(device adb.Readiness) bool {
		return device.Reason != nil && device.Reason.Code == constants.DeviceNotFound
	})
	slices.SortFunc(devices, func(a, b adb.Readiness) int {
		return strings.Compare(a.ADBSerial, b.ADBSerial)
	})
	return devices, nil
}

func copyInventory(devices []adb.Readiness) []adb.Readiness {
	result := make([]adb.Readiness, len(devices))
	copy(result, devices)
	for index, device := range result {
		if device.Reason != nil {
			reason := *device.Reason
			result[index].Reason = &reason
		}
	}
	return result
}
