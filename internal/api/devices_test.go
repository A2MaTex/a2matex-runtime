package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const (
	devicesPath        = localAPIBasePath + "/devices"
	refreshDevicesPath = devicesPath + "/refresh"
	emptyDevices       = `{"devices":[]}`
	adbUnavailableBody = `{"error":{"code":"ADB_UNAVAILABLE","message":"unable to refresh Android devices","retryable":true}}`
)

type inventorySource struct {
	discoveries int
	endpoints   []adb.Endpoint
	err         error
}

func (s *inventorySource) Discover(context.Context) ([]adb.Endpoint, error) {
	s.discoveries++
	return s.endpoints, s.err
}

func (*inventorySource) ProbeReadiness(_ context.Context, endpoint adb.Endpoint) (adb.Readiness, error) {
	device := adb.Readiness{ADBSerial: endpoint.ADBSerial}
	switch endpoint.State {
	case adb.Connected:
		device.Ready = true
	case adb.Offline:
		device.Reason = &adb.UnavailableReason{Code: constants.DeviceOffline}
	case adb.Unauthorized:
		device.Reason = &adb.UnavailableReason{Code: constants.DeviceUnauthorized}
	default:
		device.Reason = &adb.UnavailableReason{Code: constants.DeviceNotReady}
	}
	return device, nil
}

func TestDeviceInventoryRoutes(t *testing.T) {
	t.Parallel()

	source := &inventorySource{
		endpoints: []adb.Endpoint{
			{ADBSerial: "emulator-5554", State: adb.Connected},
			{ADBSerial: "Z-not-ready", State: adb.OtherUnavailable},
			{ADBSerial: "192.0.2.10:5555", State: adb.Offline},
			{ADBSerial: "A-unauthorized", State: adb.Unauthorized},
		},
	}
	router := newTestRouterWithInventory(io.Discard, services.NewDeviceInventory(source))
	request := func(method, path string) *http.Request {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", validAuthorization)
		return req
	}
	const snapshot = `{"devices":[` +
		`{"adbSerial":"192.0.2.10:5555","status":"UNAVAILABLE","error":{"code":"DEVICE_OFFLINE","message":"device is offline","retryable":true}},` +
		`{"adbSerial":"A-unauthorized","status":"UNAVAILABLE","error":{"code":"DEVICE_UNAUTHORIZED","message":"device has not authorized ADB","retryable":true}},` +
		`{"adbSerial":"Z-not-ready","status":"UNAVAILABLE","error":{"code":"DEVICE_NOT_READY","message":"device is not ready","retryable":true}},` +
		`{"adbSerial":"emulator-5554","status":"READY"}]}`

	checkDeviceResponse(t, router, request(http.MethodGet, devicesPath), http.StatusOK, emptyDevices)
	if source.discoveries != 0 {
		t.Fatal("reading the initial snapshot invoked discovery")
	}
	checkDeviceResponse(t, router, request(http.MethodPost, refreshDevicesPath), http.StatusOK, snapshot)
	checkDeviceResponse(t, router, request(http.MethodGet, devicesPath), http.StatusOK, snapshot)

	for _, failure := range []struct {
		err    error
		status int
		body   string
	}{
		{fmt.Errorf("%w: private path", xerror.ErrADBDiscovery), http.StatusServiceUnavailable, adbUnavailableBody},
		{xerror.ErrADBReadiness, http.StatusServiceUnavailable, adbUnavailableBody},
		{context.DeadlineExceeded, http.StatusServiceUnavailable, adbUnavailableBody},
		{context.Canceled, http.StatusServiceUnavailable, adbUnavailableBody},
		{errors.New("private diagnostic"), http.StatusInternalServerError, internalErrorBody},
	} {
		source.err = failure.err
		checkDeviceResponse(t, router, request(http.MethodPost, refreshDevicesPath), failure.status, failure.body)
		checkDeviceResponse(t, router, request(http.MethodGet, devicesPath), http.StatusOK, snapshot)
	}

	source.endpoints, source.err = nil, nil
	checkDeviceResponse(t, router, request(http.MethodPost, refreshDevicesPath), http.StatusOK, emptyDevices)
	checkDeviceResponse(t, router, request(http.MethodGet, devicesPath), http.StatusOK, emptyDevices)
	if source.discoveries != 7 {
		t.Fatalf("discovery calls = %d, want 7 (once per refresh)", source.discoveries)
	}
}

func TestDeviceInventoryRequestValidation(t *testing.T) {
	t.Parallel()

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, devicesPath},
		{http.MethodPost, refreshDevicesPath},
	} {
		t.Run(route.method, func(t *testing.T) {
			router := newTestRouterWithInventory(io.Discard, &unexpectedInventory{t})
			for _, input := range []struct {
				name, query, body, authorization string
				unknownLength                    bool
				status                           int
				response                         string
			}{
				{"missing token before input validation", "?x=1", "{}", "", false, http.StatusUnauthorized, unauthorizedBody},
				{"wrong token", "", "", "Bearer wrong", false, http.StatusUnauthorized, unauthorizedBody},
				{"unknown query", "?x=1", "", validAuthorization, false, http.StatusBadRequest, invalidRequestBody},
				{"body", "", "{}", validAuthorization, false, http.StatusBadRequest, invalidRequestBody},
				{"whitespace with unknown length", "", " ", validAuthorization, true, http.StatusBadRequest, invalidRequestBody},
			} {
				t.Run(input.name, func(t *testing.T) {
					req := httptest.NewRequest(route.method, route.path+input.query, strings.NewReader(input.body))
					if input.authorization != "" {
						req.Header.Set("Authorization", input.authorization)
					}
					if input.unknownLength {
						req.ContentLength = -1
					}
					checkDeviceResponse(t, router, req, input.status, input.response)
				})
			}
		})
	}
}

type unexpectedInventory struct{ t *testing.T }

func (s *unexpectedInventory) Snapshot() []adb.Readiness {
	s.t.Error("invalid request reached Snapshot")
	return nil
}

func (s *unexpectedInventory) Refresh(context.Context) ([]adb.Readiness, error) {
	s.t.Error("invalid request reached Refresh")
	return nil, nil
}

func checkDeviceResponse(t *testing.T, handler http.Handler, request *http.Request, status int, body string) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != status || response.Body.String() != body {
		t.Fatalf("%s %s = %d %s, want %d %s", request.Method, request.URL, response.Code, response.Body, status, body)
	}
	if response.Header().Get("Cache-Control") != constants.CacheControlNoStore ||
		response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("unexpected response headers: %v", response.Header())
	}
}
