package services_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

type inventorySource struct {
	discover func(context.Context) ([]adb.Endpoint, error)
	probe    func(context.Context, adb.Endpoint) (adb.Readiness, error)
}

func (s *inventorySource) Discover(ctx context.Context) ([]adb.Endpoint, error) {
	return s.discover(ctx)
}

func (s *inventorySource) ProbeReadiness(ctx context.Context, endpoint adb.Endpoint) (adb.Readiness, error) {
	return s.probe(ctx, endpoint)
}

func TestDeviceInventoryReplacesCompleteSnapshot(t *testing.T) {
	source := &inventorySource{}
	inventory := services.NewDeviceInventory(source)
	if got := inventory.Snapshot(); got == nil || len(got) != 0 {
		t.Fatalf("initial snapshot = %v", got)
	}
	source.discover = func(context.Context) ([]adb.Endpoint, error) {
		return []adb.Endpoint{
			{ADBSerial: "z", State: adb.Connected},
			{ADBSerial: "b", State: adb.Offline},
			{ADBSerial: "a", State: adb.Connected},
		}, nil
	}
	source.probe = func(_ context.Context, endpoint adb.Endpoint) (adb.Readiness, error) {
		switch endpoint.ADBSerial {
		case "z":
			return adb.Readiness{ADBSerial: "z", Ready: true}, nil
		case "b":
			return adb.Readiness{ADBSerial: "b", Reason: &adb.UnavailableReason{
				Code: constants.DeviceOffline, Message: "device is offline",
			}}, nil
		default:
			return adb.Readiness{ADBSerial: "a", Reason: &adb.UnavailableReason{
				Code: constants.DeviceNotFound, Message: "device is no longer connected",
			}}, nil
		}
	}

	want := []adb.Readiness{
		{ADBSerial: "b", Reason: &adb.UnavailableReason{Code: constants.DeviceOffline, Message: "device is offline"}},
		{ADBSerial: "z", Ready: true},
	}
	got, err := inventory.Refresh(t.Context())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("refresh = %+v, %v, want %+v", got, err, want)
	}
	got[0].ADBSerial = "changed"
	got[0].Reason.Code = "changed"
	inventory.Snapshot()[0].Reason.Message = "changed"
	if snapshot := inventory.Snapshot(); !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("mutable snapshot = %+v, want %+v", snapshot, want)
	}

	source.discover = func(context.Context) ([]adb.Endpoint, error) { return []adb.Endpoint{}, nil }
	got, err = inventory.Refresh(t.Context())
	if err != nil || got == nil || len(got) != 0 || len(inventory.Snapshot()) != 0 {
		t.Fatalf("empty refresh = %+v, %v", got, err)
	}
}

func TestDeviceInventorySharesRefreshAndLimitsProbes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		source, inventory := inventoryWithPreviousSnapshot(t)
		block := make(chan struct{})
		var active, peak, discoveries atomic.Int32
		source.discover = func(context.Context) ([]adb.Endpoint, error) {
			discoveries.Add(1)
			endpoints := make([]adb.Endpoint, constants.DeviceInventoryProbeLimit+2)
			for i := range endpoints {
				endpoints[i] = adb.Endpoint{ADBSerial: fmt.Sprintf("device-%d", i), State: adb.Connected}
			}
			return endpoints, nil
		}
		source.probe = func(ctx context.Context, endpoint adb.Endpoint) (adb.Readiness, error) {
			count := active.Add(1)
			defer active.Add(-1)
			for previous := peak.Load(); count > previous && !peak.CompareAndSwap(previous, count); previous = peak.Load() {
			}
			select {
			case <-block:
				return adb.Readiness{ADBSerial: endpoint.ADBSerial, Ready: true}, nil
			case <-ctx.Done():
				return adb.Readiness{}, ctx.Err()
			}
		}

		type response struct {
			devices []adb.Readiness
			err     error
		}
		done := make(chan response, 2)
		refresh := func() {
			devices, err := inventory.Refresh(t.Context())
			done <- response{devices, err}
		}
		go refresh()
		synctest.Wait()
		if devices := inventory.Snapshot(); len(devices) != 1 || devices[0].ADBSerial != "previous" {
			t.Fatalf("snapshot during refresh = %+v", devices)
		}

		go refresh()
		ctx, cancel := context.WithCancel(t.Context())
		cancelled := make(chan error, 1)
		go func() {
			_, err := inventory.Refresh(ctx)
			cancelled <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-cancelled; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter error = %v", err)
		}
		close(block)

		first, second := <-done, <-done
		if first.err != nil || second.err != nil || !reflect.DeepEqual(first.devices, second.devices) ||
			len(first.devices) != constants.DeviceInventoryProbeLimit+2 {
			t.Fatalf("refreshes = %+v, %+v", first, second)
		}
		if discoveries.Load() != 1 || peak.Load() != constants.DeviceInventoryProbeLimit || active.Load() != 0 {
			t.Fatalf("discoveries=%d peak=%d active=%d", discoveries.Load(), peak.Load(), active.Load())
		}
		first.devices[0].ADBSerial = "changed"
		if second.devices[0].ADBSerial == "changed" {
			t.Fatal("shared refresh leaked a mutable snapshot between callers")
		}
	})
}

func TestDeviceInventoryRetainsSnapshotOnFailure(t *testing.T) {
	failure := errors.New("ADB operation failed")
	for _, name := range []string{"discovery failure", "probe failure", "caller cancellation", "refresh deadline"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				source, inventory := inventoryWithPreviousSnapshot(t)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				source.discover = func(context.Context) ([]adb.Endpoint, error) {
					if name == "discovery failure" {
						return nil, failure
					}
					return []adb.Endpoint{{ADBSerial: "new", State: adb.Connected}}, nil
				}
				want := failure
				switch name {
				case "caller cancellation":
					want = context.Canceled
				case "refresh deadline":
					want = context.DeadlineExceeded
				}
				source.probe = func(probeCtx context.Context, endpoint adb.Endpoint) (adb.Readiness, error) {
					switch name {
					case "caller cancellation":
						cancel()
						return adb.Readiness{ADBSerial: endpoint.ADBSerial, Ready: true}, nil
					case "refresh deadline":
						<-probeCtx.Done()
						return adb.Readiness{}, probeCtx.Err()
					default:
						return adb.Readiness{}, failure
					}
				}
				started := time.Now()
				if got, err := inventory.Refresh(ctx); got != nil || !errors.Is(err, want) {
					t.Fatalf("refresh = %+v, %v, want %v", got, err, want)
				}
				if name == "refresh deadline" && time.Since(started) != constants.DeviceInventoryRefreshTimeout {
					t.Fatalf("elapsed = %v", time.Since(started))
				}
				if got := inventory.Snapshot(); len(got) != 1 || got[0].ADBSerial != "previous" {
					t.Fatalf("failed refresh changed snapshot: %+v", got)
				}
				source.discover = func(context.Context) ([]adb.Endpoint, error) { return nil, nil }
				if got, err := inventory.Refresh(t.Context()); err != nil || got == nil || len(got) != 0 {
					t.Fatalf("retry = %+v, %v", got, err)
				}
			})
		})
	}
}

func inventoryWithPreviousSnapshot(t *testing.T) (*inventorySource, *services.DeviceInventory) {
	t.Helper()
	source := &inventorySource{
		discover: func(context.Context) ([]adb.Endpoint, error) {
			return []adb.Endpoint{{ADBSerial: "previous", State: adb.Connected}}, nil
		},
		probe: func(_ context.Context, endpoint adb.Endpoint) (adb.Readiness, error) {
			return adb.Readiness{ADBSerial: endpoint.ADBSerial, Ready: true}, nil
		},
	}
	inventory := services.NewDeviceInventory(source)
	if _, err := inventory.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	return source, inventory
}
