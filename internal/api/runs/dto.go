package runs

import (
	"encoding/json"

	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
)

// SnapshotResponse is the existing A7 projection, detached from its read transaction.
type SnapshotResponse = execution.Snapshot

type StartRequest struct {
	RequestID string             `json:"requestId"`
	RuntimeIR json.RawMessage    `json:"runtimeIr"`
	Devices   []StartDeviceInput `json:"devices"`
}

type StartDeviceInput struct {
	ADBSerial string          `json:"adbSerial"`
	Inputs    json.RawMessage `json:"inputs"`
}

type AdmissionErrorResponse struct {
	Error      apiresponse.Error  `json:"error"`
	Validation *validation.Result `json:"validation,omitempty"`
}
