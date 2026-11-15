//go:build integration

package services_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const (
	readySerial   = "emulator-5554"
	secondSerial  = "emulator-5556"
	offlineSerial = "emulator-5558"
	listHeader    = "List of devices attached\n"
	fixtureADB    = "phase-e-test-adb"
)

// The copied test binary serves as ADB so E1 receives the actual fixed
// arguments from E2–E4 on both supported hosts.
func TestMain(m *testing.M) {
	binary, err := os.Executable()
	if err == nil && (filepath.Base(binary) == fixtureADB || filepath.Base(binary) == fixtureADB+".exe") {
		os.Exit(serveADBFixture(binary, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func serveADBFixture(binary string, args []string) int {
	switch {
	case slices.Equal(args, []string{"version"}):
		fmt.Println("Android Debug Bridge version 1.0.41")
	case slices.Equal(args, []string{"devices", "-l"}):
		data, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "devices.txt"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Print(string(data))
	case slices.Equal(args, []string{"-s", readySerial, "shell", "-T", "getprop", "sys.boot_completed"}),
		slices.Equal(args, []string{"-s", secondSerial, "shell", "-T", "getprop", "sys.boot_completed"}):
		fmt.Println("1")
	default:
		fmt.Fprintln(os.Stderr, "unexpected ADB arguments", args)
		return 2
	}
	return 0
}

func TestDeviceSubsystemIntegration(t *testing.T) {
	home := t.TempDir()
	name := fixtureADB
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(home, name)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, data, 0700); err != nil {
		t.Fatal(err)
	}
	setDevices := func(output string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, "devices.txt"), []byte(output), 0600); err != nil {
			t.Fatal(err)
		}
	}

	resolver := adb.NewResolver(config.ADBConfig{ExecutablePath: binary})
	inventory := services.NewDeviceInventory(resolver)
	preflight := services.NewDevicePreflight(resolver)
	if initial := inventory.Snapshot(); initial == nil || len(initial) != 0 {
		t.Fatalf("initial inventory = %+v", initial)
	}

	setDevices(listHeader + offlineSerial + "\toffline\n" + secondSerial + "\tdevice\n" + readySerial + "\tdevice model:Fixture\n")
	snapshot, err := inventory.Refresh(t.Context())
	if err != nil || len(snapshot) != 3 || snapshot[0] != (adb.Readiness{ADBSerial: readySerial, Ready: true}) ||
		snapshot[1] != (adb.Readiness{ADBSerial: secondSerial, Ready: true}) || snapshot[2].ADBSerial != offlineSerial ||
		snapshot[2].Ready || snapshot[2].Reason == nil || snapshot[2].Reason.Code != constants.DeviceOffline {
		t.Fatalf("refresh = %+v, %v", snapshot, err)
	}
	selection := []string{secondSerial, readySerial}
	accepted, err := preflight.Check(t.Context(), selection)
	if err != nil || !slices.Equal(accepted.AcceptedSerials, selection) || len(accepted.Unavailable) != 0 {
		t.Fatalf("preflight = %+v, %v", accepted, err)
	}

	setDevices(listHeader + readySerial + "\tdevice\n")
	rejected, err := preflight.Check(t.Context(), selection)
	if err != nil || len(rejected.AcceptedSerials) != 0 || len(rejected.Unavailable) != 1 ||
		rejected.Unavailable[0].Index != 0 || rejected.Unavailable[0].ADBSerial != secondSerial ||
		rejected.Unavailable[0].Reason.Code != constants.DeviceNotFound {
		t.Fatalf("preflight after disconnect = %+v, %v", rejected, err)
	}
	if !reflect.DeepEqual(inventory.Snapshot(), snapshot) {
		t.Fatal("preflight changed the published inventory")
	}

	setDevices(listHeader + "bad-row\n")
	if got, err := inventory.Refresh(t.Context()); got != nil || !errors.Is(err, xerror.ErrADBDiscovery) {
		t.Fatalf("malformed discovery refresh = %+v, %v", got, err)
	}
	if !reflect.DeepEqual(inventory.Snapshot(), snapshot) {
		t.Fatal("failed refresh replaced the published inventory")
	}

	setDevices(listHeader)
	if got, err := inventory.Refresh(t.Context()); err != nil || got == nil || len(got) != 0 || len(inventory.Snapshot()) != 0 {
		t.Fatalf("empty refresh = %+v, %v", got, err)
	}
}

func TestLiveDeviceSubsystemIntegration(t *testing.T) {
	executable := os.Getenv("A2MATEX_TEST_ADB_PATH")
	serial := os.Getenv("A2MATEX_TEST_ADB_SERIAL")
	if executable == "" || serial == "" {
		t.Skip("set A2MATEX_TEST_ADB_PATH and A2MATEX_TEST_ADB_SERIAL to check a live device")
	}
	resolver := adb.NewResolver(config.ADBConfig{ExecutablePath: executable})
	inventory := services.NewDeviceInventory(resolver)
	devices, err := inventory.Refresh(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(devices, func(device adb.Readiness) bool { return device.ADBSerial == serial })
	if index < 0 || !devices[index].Ready {
		t.Fatalf("selected device not ready in inventory (found=%t)", index >= 0)
	}
	selection, err := services.NewDevicePreflight(resolver).Check(t.Context(), []string{serial})
	if err != nil || !slices.Equal(selection.AcceptedSerials, []string{serial}) {
		t.Fatalf("live preflight accepted %d devices, error=%v", len(selection.AcceptedSerials), err)
	}
}
