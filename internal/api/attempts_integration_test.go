package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	validationservice "github.com/A2MaTex/a2matex-runtime/internal/validation/service"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	"github.com/google/uuid"
)

func historyPage(t *testing.T, f *snapshotFixture, deviceID, query string, status int) execution.AttemptPage {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/device-executions/"+deviceID+"/attempts"+query, nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	out := httptest.NewRecorder()
	f.router.ServeHTTP(out, req)
	if out.Code != status || out.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("attempt history", out.Code, out.Body.String())
	}
	var page execution.AttemptPage
	if status == 200 {
		if err := json.Unmarshal(out.Body.Bytes(), &page); err != nil || page.Attempts == nil {
			t.Fatal("invalid page projection", out.Body.String(), err)
		}
	}
	return page
}

func TestHistoryCompiledMultiDeviceRunPagingAndActiveRefresh(t *testing.T) {
	f := multiDeviceStart(t, 2)
	raw, err := os.ReadFile("../../examples/h4/compile-workflow.json")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow-compilation", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	f.router.ServeHTTP(out, req)
	var compiled validationservice.CompilationResult
	if err := json.Unmarshal(out.Body.Bytes(), &compiled); err != nil || out.Code != 200 || compiled.Validation.Status != "VALID" {
		t.Fatal("compile history fixture", out.Code, out.Body.String(), err)
	}
	f.request.RuntimeIR = compiled.RuntimeIR
	for i := range f.request.Devices {
		f.request.Devices[i].Inputs = json.RawMessage(fmt.Sprintf(`{"delayMs":%d}`, i+1))
	}
	created := f.post(t, t.Context(), f.request, 201)
	for range 2 {
		select {
		case <-f.entered:
		case <-time.After(time.Second):
			t.Fatal("both devices did not reach the gated Delay")
		}
	}
	ids := make([]string, 2)
	for i, device := range created.DeviceExecutions {
		page := historyPage(t, f.snapshotFixture, device.ID, "?afterSequence=1", 200)
		if len(page.Attempts) != 1 || page.Attempts[0].NodeID != "wait" || page.Attempts[0].Status != constants.ExecutionRunning || string(page.Attempts[0].Output) != "null" || string(page.Attempts[0].Error) != "null" || page.Attempts[0].FinishedAt != nil || page.NextAfterSequence != nil {
			t.Fatal("active page", page)
		}
		ids[i] = page.Attempts[0].ID
		if page := historyPage(t, f.snapshotFixture, device.ID, "?afterSequence=2", 200); len(page.Attempts) != 0 {
			t.Fatal("sequence incorrectly acts as an update cursor", page)
		}
	}
	// Cancel only the read, while the same held execution continues normally.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	read := httptest.NewRequest(http.MethodGet, "/api/v1/device-executions/"+created.DeviceExecutions[0].ID+"/attempts", nil).WithContext(ctx)
	read.Header.Set("Authorization", "Bearer "+f.token)
	readResponse := httptest.NewRecorder()
	f.router.ServeHTTP(readResponse, read)
	if readResponse.Code != 500 {
		t.Fatal("cancelled read became successful", readResponse.Code)
	}
	close(f.resume)
	final := f.settled(t, created.RunGroup.ID, constants.ExecutionSucceeded)
	before, scans, calls := f.counts(t), f.source.calls.Load(), f.delayCalls.Load()
	for i, device := range final.DeviceExecutions {
		for j, node := range []string{"start", "wait", "end"} {
			page := historyPage(t, f.snapshotFixture, device.ID, fmt.Sprintf("?afterSequence=%d&limit=1", j), 200)
			if len(page.Attempts) != 1 || page.Attempts[0].DeviceExecutionID != device.ID || page.Attempts[0].Sequence != int64(j+1) || page.Attempts[0].NodeID != node || page.Attempts[0].Status != constants.ExecutionSucceeded {
				t.Fatal("wrong page or owner", page)
			}
			if j < 2 && (page.NextAfterSequence == nil || *page.NextAfterSequence != int64(j+1)) || j == 2 && page.NextAfterSequence != nil {
				t.Fatal("last-full-page continuation", page)
			}
			if j == 1 && (page.Attempts[0].ID != ids[i] || string(page.Attempts[0].Output) != fmt.Sprintf(`{"durationMs":%d}`, i+1)) {
				t.Fatal("lost active update or private result", page)
			}
		}
		if page := historyPage(t, f.snapshotFixture, device.ID, "?afterSequence=9223372036854775807", 200); len(page.Attempts) != 0 || page.NextAfterSequence != nil {
			t.Fatal("beyond last sequence", page)
		}
	}
	historyPage(t, f.snapshotFixture, uuid.NewString(), "", 404)
	if f.counts(t) != before || f.source.calls.Load() != scans || f.delayCalls.Load() != calls {
		t.Fatal("history GET wrote records/events or invoked ADB/handlers")
	}
	for _, tc := range []struct{ method, suffix string }{{"HEAD", ""}, {"POST", ""}, {"GET", "/"}, {"GET", "/extra"}} {
		t.Run(tc.method+tc.suffix, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/v1/device-executions/"+final.DeviceExecutions[0].ID+"/attempts"+tc.suffix, nil)
			req.Header.Set("Authorization", "Bearer "+f.token)
			out := httptest.NewRecorder()
			f.router.ServeHTTP(out, req)
			if out.Code != 404 || out.Header().Get("Location") != "" {
				t.Fatal("history route introduced an alias/redirect", out.Code)
			}
		})
	}
}

func TestHistoryKnownEmptyDeviceAndAuthentication(t *testing.T) {
	f := startStore(t)
	f.admit(t)
	id := f.admission.Devices[0].DeviceExecutionID
	before := f.counts(t)
	page := historyPage(t, f.snapshotFixture, id, "", 200)
	if len(page.Attempts) != 0 || page.NextAfterSequence != nil || f.counts(t) != before {
		t.Fatal("empty history changed state", page)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/device-executions/invalid/attempts?limit=0", nil)
	out := httptest.NewRecorder()
	f.router.ServeHTTP(out, req)
	if out.Code != 401 || !strings.Contains(out.Body.String(), `"code":"UNAUTHORIZED"`) {
		t.Fatal("real router auth order", out.Code, out.Body.String())
	}
}
