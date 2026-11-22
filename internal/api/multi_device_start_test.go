package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/api/runs"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

func multiDeviceStart(t *testing.T, count int) *startFixture {
	t.Helper()
	f := startStore(t)
	f.entered = make(chan registry.Invocation, count*2)
	f.source.serials = make([]string, count)
	f.request.Devices = make([]runs.StartDeviceInput, count)
	for i := range count {
		serial := fmt.Sprintf("device-%d", i)
		f.source.serials[i] = serial
		f.request.Devices[i] = runs.StartDeviceInput{ADBSerial: serial, Inputs: json.RawMessage(fmt.Sprintf(`{"duration":%d}`, i+1))}
	}
	return f
}

func TestMultiDeviceStartHTTPAndReplayKeepEveryInputPair(t *testing.T) {
	for _, count := range []int{2, 33} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := multiDeviceStart(t, count)
			ctx, cancel := context.WithCancel(t.Context())
			created := f.post(t, ctx, f.request, http.StatusCreated)
			cancel() // Owned workers must survive the submitting request.
			if len(created.DeviceExecutions) != count || created.RunGroup.Summary.Total != count {
				t.Fatal("incomplete admitted snapshot", created)
			}
			for range count {
				select {
				case call := <-f.entered:
					var duration int
					if err := json.Unmarshal(call.Inputs["durationMs"], &duration); err != nil || duration < 1 || duration > count || call.ADBSerial != f.request.Devices[duration-1].ADBSerial {
						t.Fatal("serial/input pairing changed", call.ADBSerial, err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("selection was throttled or only its first member started")
				}
			}
			for i, device := range created.DeviceExecutions {
				if device.ADBSerial != f.request.Devices[i].ADBSerial {
					t.Fatal("snapshot reordered selection")
				}
			}
			replay := f.request
			replay.RuntimeIR, replay.Devices = json.RawMessage(`{}`), []runs.StartDeviceInput{}
			before := f.counts(t) // Every worker is held at its first handler.
			scans := f.source.calls.Load()
			if saved := f.post(t, t.Context(), replay, http.StatusOK); saved.RunGroup.ID != created.RunGroup.ID {
				t.Fatal("active replay changed identity")
			}
			if f.counts(t) != before || f.source.calls.Load() != scans || f.delayCalls.Load() != int32(count) {
				t.Fatal("active replay created work or rescanned ADB")
			}
			close(f.resume)
			finished := f.settled(t, created.RunGroup.ID, constants.ExecutionSucceeded)
			if finished.RunGroup.Summary.Succeeded != count {
				t.Fatal("not every device finished", finished)
			}
			before = f.counts(t)
			f.post(t, t.Context(), replay, http.StatusOK)
			if after := f.counts(t); after != before || after[0] != 1 || after[1] != count || after[2] != 4*count || f.delayCalls.Load() != int32(2*count) {
				t.Fatal("terminal replay changed records or execution", before, after)
			}
		})
	}
}

func TestMultiDeviceStartRejectsWholeInvalidSelection(t *testing.T) {
	for _, name := range []string{"empty", "duplicate", "bad serial", "later input", "later missing", "multiple offline"} {
		t.Run(name, func(t *testing.T) {
			f := multiDeviceStart(t, 3)
			status := http.StatusBadRequest
			switch name {
			case "empty":
				f.request.Devices = []runs.StartDeviceInput{}
			case "duplicate":
				f.request.Devices[2].ADBSerial = f.request.Devices[0].ADBSerial
			case "bad serial":
				f.request.Devices[2].ADBSerial = "invalid serial"
			case "later input":
				status = http.StatusUnprocessableEntity
				f.request.Devices[2].Inputs = json.RawMessage(`{}`)
			case "later missing":
				status = http.StatusUnprocessableEntity
				f.request.Devices[2].ADBSerial = "vanished"
			case "multiple offline":
				status = http.StatusUnprocessableEntity
				f.source.offline = map[string]bool{f.source.serials[1]: true, f.source.serials[2]: true}
			}
			f.post(t, t.Context(), f.request, status)
			if f.counts(t) != [4]int{} || f.delayCalls.Load() != 0 {
				t.Fatal("rejected selection partially admitted or dispatched")
			}
			if name == "multiple offline" || name == "later missing" {
				body, _ := json.Marshal(f.request)
				req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+f.token)
				rec := httptest.NewRecorder()
				f.router.ServeHTTP(rec, req)
				var response runs.AdmissionErrorResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				want := 1
				if name == "multiple offline" {
					want = 2
				}
				if len(response.UnavailableDevices) != want || response.Validation != nil {
					t.Fatal("missing selection diagnostics", response)
				}
				for i, issue := range response.UnavailableDevices {
					wantIndex := 2
					if name == "multiple offline" {
						wantIndex = i + 1
					}
					if issue.Index != wantIndex || issue.ADBSerial != f.request.Devices[issue.Index].ADBSerial || !issue.Reason.Retryable {
						t.Fatal("incorrect selection diagnostic", issue)
					}
				}
			}
		})
	}
}
