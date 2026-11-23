package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	"github.com/google/uuid"
)

func runAction(t *testing.T, f *startFixture, id, action string, status int) execution.Snapshot {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+id+"/actions", strings.NewReader(`{"action":"`+action+`"}`))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	f.router.ServeHTTP(out, req)
	if out.Code != status || out.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("action %s: %d %s", action, out.Code, out.Body.String())
	}
	var result execution.Snapshot
	if status == 200 || status == 202 {
		if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil || result.RunGroup.ID != id {
			t.Fatal("action snapshot", out.Body.String(), err)
		}
	} else if status == 409 {
		var conflict struct {
			Error    struct{ Code string }
			Snapshot execution.Snapshot
		}
		if err := json.Unmarshal(out.Body.Bytes(), &conflict); err != nil || conflict.Error.Code != constants.APIErrorCodeInvalidExecutionState || conflict.Snapshot.RunGroup.ID != id {
			t.Fatal("conflict shape", out.Body.String(), err)
		}
		result = conflict.Snapshot
	}
	return result
}

func TestRunActionsRealGroupPauseResumeCancelAndFreshRun(t *testing.T) {
	f := multiDeviceStart(t, 2)
	created := f.post(t, t.Context(), f.request, 201)
	for range 2 {
		select {
		case <-f.entered:
		case <-time.After(time.Second):
			t.Fatal("both workers must enter before controls")
		}
	}
	id := created.RunGroup.ID
	runAction(t, f, id, "PAUSE", 202)
	runAction(t, f, id, "PAUSE", 202)
	runAction(t, f, id, "RESUME", 409) // Permitted handlers still held: Pause is definitely pending.
	close(f.resume)
	f.settled(t, id, constants.ExecutionPaused)
	// The PAUSED transaction can precede the in-memory coordinator acknowledgement.
	deadline := time.Now().Add(time.Second)
	for {
		response := actionStatus(f, id, "PAUSE")
		if response.Code == 200 {
			break
		}
		if response.Code != 202 || time.Now().After(deadline) {
			t.Fatal("Pause never acknowledged", response.Code, response.Body.String())
		}
		time.Sleep(time.Millisecond)
	}
	for _, device := range created.DeviceExecutions {
		page, err := f.repo.Attempts(t.Context(), device.ID, 0, 0)
		if err != nil || len(page.Attempts) != 2 || page.Attempts[1].Status != constants.ExecutionSucceeded {
			t.Fatal("parked execution advanced", page, err)
		}
	}
	runAction(t, f, id, "RESUME", 202)
	final := f.settled(t, id, constants.ExecutionSucceeded)
	if final.RunGroup.Summary.Succeeded != 2 {
		t.Fatal(final)
	}
	for range 2 {
		select {
		case <-f.entered:
		case <-time.After(time.Second):
			t.Fatal("consumer callback missing")
		}
	}
	for _, device := range created.DeviceExecutions {
		page, err := f.repo.Attempts(t.Context(), device.ID, 0, 0)
		if err != nil || len(page.Attempts) != 4 {
			t.Fatal("Resume repeated or lost attempts", page, err)
		}
	}
	runAction(t, f, id, "CANCEL", 409)
	f.request.RequestID = uuid.NewString()
	for i := range f.request.Devices {
		f.request.Devices[i].Inputs = json.RawMessage(`{"duration":500}`)
	}
	second := f.post(t, t.Context(), f.request, 201)
	for range 2 {
		select {
		case <-f.entered:
		case <-time.After(time.Second):
			t.Fatal("new run not executing")
		}
	}
	runAction(t, f, second.RunGroup.ID, "CANCEL", 202)
	f.settled(t, second.RunGroup.ID, constants.ExecutionCancelled)
	deadline = time.Now().Add(time.Second)
	for {
		response := actionStatus(f, second.RunGroup.ID, "CANCEL")
		if response.Code == 200 {
			break
		}
		if response.Code != 202 || time.Now().After(deadline) {
			t.Fatal("Cancel never retired", response.Code, response.Body.String())
		}
		time.Sleep(time.Millisecond)
	}
	before := f.counts(t)
	runAction(t, f, second.RunGroup.ID, "CANCEL", 200)
	if f.counts(t) != before {
		t.Fatal("retired duplicate created events/work")
	}
	f.request.RequestID = uuid.NewString()
	for i := range f.request.Devices {
		f.request.Devices[i].Inputs = json.RawMessage(`{"duration":0}`)
	}
	third := f.post(t, t.Context(), f.request, 201)
	if third.RunGroup.ID == id || third.RunGroup.ID == second.RunGroup.ID {
		t.Fatal("new run reused identity")
	}
	f.settled(t, third.RunGroup.ID, constants.ExecutionSucceeded)
	runAction(t, f, uuid.NewString(), "CANCEL", 404)
	if strings.Contains(f.logs.String(), f.token) {
		t.Fatal("action log exposed credential")
	}
}

func actionStatus(f *startFixture, id, action string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+id+"/actions", strings.NewReader(`{"action":"`+action+`"}`))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	f.router.ServeHTTP(out, req)
	return out
}

func TestRunActionRouterAuthAndContextualErrorLog(t *testing.T) {
	f := startStore(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/bad/actions", strings.NewReader("invalid"))
	out := httptest.NewRecorder()
	f.router.ServeHTTP(out, req)
	if out.Code != 401 {
		t.Fatal("auth did not precede validation", out.Code)
	}
	lines := strings.Split(strings.TrimSpace(f.logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatal("duplicate request logs", f.logs.String())
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil || entry["level"] != "ERROR" || entry["route"] != "/api/v1/runs/:runGroupId/actions" {
		t.Fatal(entry, err)
	}
	f.logs.Reset()
	f.admit(t) // A saved group without a live owner cannot be resumed.
	runAction(t, f, f.admission.RunGroupID, "RESUME", 409)
	if strings.Count(f.logs.String(), "\n") != 1 || !strings.Contains(f.logs.String(), `"operation":"request_group_control"`) || !strings.Contains(f.logs.String(), `"error_code":"INVALID_EXECUTION_STATE"`) {
		t.Fatal("missing contextual conflict log", f.logs.String())
	}
}
