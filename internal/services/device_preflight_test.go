package services_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestDevicePreflightValidatesBeforeDiscovery(t *testing.T) {
	preflight := services.NewDevicePreflight(&inventorySource{})
	for _, tc := range []struct {
		name    string
		serials []string
		detail  string
	}{
		{"empty", nil, "at least one"},
		{"duplicate", []string{"serial", "serial"}, "index 1"},
		{"blank", []string{""}, "index 0"},
		{"whitespace", []string{"a", "serial "}, "index 1"},
		{"invalid UTF-8", []string{"\xff"}, "index 0"},
		{"control", []string{"serial\x00"}, "index 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := preflight.Check(t.Context(), tc.serials)
			if !errors.Is(err, xerror.ErrInvalidDeviceSelection) || !strings.Contains(err.Error(), tc.detail) ||
				got.AcceptedSerials != nil || got.Unavailable != nil {
				t.Fatalf("selection = %+v, error = %v", got, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := preflight.Check(ctx, []string{"serial"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled check = %v", err)
	}
}

func TestDevicePreflightAcceptsOnlySelectedSerialsInOrder(t *testing.T) {
	for _, count := range []int{1, 128} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			endpoints := []adb.Endpoint{{ADBSerial: "unselected", State: adb.Connected}}
			serials := make([]string, count)
			for i := range serials {
				serial := fmt.Sprintf("[2001:db8::%x]:5555", i+1)
				endpoints = append(endpoints, adb.Endpoint{ADBSerial: serial, State: adb.Connected})
				serials[count-1-i] = serial
			}
			var discoveries int
			var probes atomic.Int32
			source := &inventorySource{
				discover: func(context.Context) ([]adb.Endpoint, error) {
					discoveries++
					return endpoints, nil
				},
				probe: func(_ context.Context, endpoint adb.Endpoint) (adb.Readiness, error) {
					probes.Add(1)
					if endpoint.ADBSerial == "unselected" {
						return adb.Readiness{}, errors.New("unselected endpoint probed")
					}
					return adb.Readiness{ADBSerial: endpoint.ADBSerial, Ready: true}, nil
				},
			}
			got, err := services.NewDevicePreflight(source).Check(t.Context(), serials)
			if err != nil || !slices.Equal(got.AcceptedSerials, serials) || len(got.Unavailable) != 0 ||
				discoveries != 1 || probes.Load() != int32(count) {
				t.Fatalf("selection = %+v, error = %v, discovery=%d probes=%d", got, err, discoveries, probes.Load())
			}
			got.AcceptedSerials[0] = "changed"
			if serials[0] == "changed" {
				t.Fatal("result aliases input")
			}
		})
	}
}

func TestDevicePreflightReportsUnavailableWithoutChangingInventory(t *testing.T) {
	source, inventory := inventoryWithPreviousSnapshot(t)
	source.discover = func(context.Context) ([]adb.Endpoint, error) {
		return []adb.Endpoint{{ADBSerial: "offline", State: adb.Offline}, {ADBSerial: "vanished", State: adb.Connected}}, nil
	}
	source.probe = func(_ context.Context, endpoint adb.Endpoint) (adb.Readiness, error) {
		code := constants.DeviceNotFound
		if endpoint.State == adb.Offline {
			code = constants.DeviceOffline
		}
		return adb.Readiness{ADBSerial: endpoint.ADBSerial, Reason: &adb.UnavailableReason{Code: code, Message: "unavailable"}}, nil
	}
	serials := []string{"vanished", "previous", "offline"}
	got, err := services.NewDevicePreflight(source).Check(t.Context(), serials)
	if err != nil || got.AcceptedSerials != nil || len(got.Unavailable) != 3 {
		t.Fatalf("selection = %+v, error = %v", got, err)
	}
	for index, code := range []string{constants.DeviceNotFound, constants.DeviceNotFound, constants.DeviceOffline} {
		issue := got.Unavailable[index]
		if issue.Index != index || issue.ADBSerial != serials[index] || issue.Reason.Code != code || issue.Reason.Message == "" {
			t.Fatalf("issue %d = %+v", index, issue)
		}
	}
	if snapshot := inventory.Snapshot(); len(snapshot) != 1 || snapshot[0].ADBSerial != "previous" || !snapshot[0].Ready {
		t.Fatalf("preflight changed inventory: %+v", snapshot)
	}
}

func TestDevicePreflightCancelsAndJoinsOtherProbes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failure := errors.New("selected probe failed")
		started := make(chan struct{}, constants.DevicePreflightProbeLimit)
		fail := make(chan struct{})
		var finished atomic.Int32
		serials := []string{"0", "1", "2", "3", "4"}
		source := &inventorySource{
			discover: func(context.Context) ([]adb.Endpoint, error) {
				endpoints := make([]adb.Endpoint, len(serials))
				for i, serial := range serials {
					endpoints[i] = adb.Endpoint{ADBSerial: serial, State: adb.Connected}
				}
				return endpoints, nil
			},
			probe: func(ctx context.Context, endpoint adb.Endpoint) (adb.Readiness, error) {
				started <- struct{}{}
				defer finished.Add(1)
				if endpoint.ADBSerial == "0" {
					<-fail
					return adb.Readiness{}, failure
				}
				<-ctx.Done()
				return adb.Readiness{}, ctx.Err()
			},
		}
		done := make(chan error, 1)
		go func() {
			result, err := services.NewDevicePreflight(source).Check(t.Context(), serials)
			if result.AcceptedSerials != nil || result.Unavailable != nil {
				t.Errorf("failure returned a partial result: %+v", result)
			}
			done <- err
		}()
		synctest.Wait()
		count := len(started)
		close(fail)
		if err := <-done; !errors.Is(err, failure) || count != constants.DevicePreflightProbeLimit ||
			len(started) != count || finished.Load() != int32(count) {
			t.Fatalf("error=%v started=%d finished=%d", err, len(started), finished.Load())
		}
	})
}

func TestDevicePreflightPropagatesDiscoveryFailureAndDeadline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		want    error
	}{
		{"discovery failure", time.Minute, xerror.ErrADBDiscovery},
		{"caller deadline", time.Second, context.DeadlineExceeded},
		{"preflight budget", time.Minute, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				source := &inventorySource{discover: func(ctx context.Context) ([]adb.Endpoint, error) {
					if tc.want == xerror.ErrADBDiscovery {
						return nil, tc.want
					}
					<-ctx.Done()
					return nil, ctx.Err()
				}}
				ctx, cancel := context.WithTimeout(t.Context(), tc.timeout)
				defer cancel()
				start := time.Now()
				got, err := services.NewDevicePreflight(source).Check(ctx, []string{"serial"})
				if !errors.Is(err, tc.want) || got.AcceptedSerials != nil || got.Unavailable != nil {
					t.Fatalf("selection = %+v, error = %v", got, err)
				}
				if tc.want == context.DeadlineExceeded && time.Since(start) != min(tc.timeout, constants.DevicePreflightTimeout) {
					t.Fatalf("elapsed = %v", time.Since(start))
				}
			})
		})
	}
}
