package devices

import (
	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

type inventoryResponse struct {
	Devices []deviceResponse `json:"devices"`
}

type deviceResponse struct {
	ADBSerial string             `json:"adbSerial"`
	Status    string             `json:"status"`
	Error     *apiresponse.Error `json:"error,omitempty"`
}

func newInventoryResponse(devices []adb.Readiness) inventoryResponse {
	result := inventoryResponse{Devices: make([]deviceResponse, len(devices))}
	for index, device := range devices {
		entry := deviceResponse{ADBSerial: device.ADBSerial, Status: constants.APIDeviceStatusReady}
		if !device.Ready {
			entry.Status = constants.APIDeviceStatusUnavailable
			entry.Error = unavailableError(device.Reason.Code)
		}
		result.Devices[index] = entry
	}
	return result
}

func unavailableError(code string) *apiresponse.Error {
	result := &apiresponse.Error{Code: code, Retryable: true}
	switch code {
	case constants.DeviceOffline:
		result.Message = "device is offline"
	case constants.DeviceUnauthorized:
		result.Message = "device has not authorized ADB"
	case constants.DeviceNotReady:
		result.Message = "device is not ready"
	default:
		panic("invalid device inventory reason")
	}
	return result
}
