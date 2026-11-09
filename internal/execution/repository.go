package execution

import (
	"context"
	"encoding/json"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	"time"
)

type Admission struct {
	RequestID  string
	RunGroupID string
	Now        time.Time
	Devices    []AdmissionDevice
}

type AdmissionDevice struct {
	DeviceExecutionID string
	ADBSerial         string
	EntryNodeID       string
}

type AdmissionResult struct {
	Disposition constants.AdmissionDisposition
	RunGroupID  string
}

type Snapshot struct {
	Cursor           string            `json:"cursor"`
	RunGroup         RunGroup          `json:"runGroup"`
	DeviceExecutions []DeviceExecution `json:"deviceExecutions"`
}

type RunGroup struct {
	ID         string     `json:"id"`
	RequestID  string     `json:"requestId"`
	Status     string     `json:"status"`
	CreatedAt  string     `json:"createdAt"`
	FinishedAt *string    `json:"finishedAt"`
	Summary    RunSummary `json:"summary"`
}

type RunSummary struct {
	Total       int `json:"total"`
	Queued      int `json:"queued"`
	Running     int `json:"running"`
	Paused      int `json:"paused"`
	Succeeded   int `json:"succeeded"`
	Failed      int `json:"failed"`
	Cancelled   int `json:"cancelled"`
	Interrupted int `json:"interrupted"`
}

type DeviceExecution struct {
	ID            string          `json:"id"`
	ADBSerial     string          `json:"adbSerial"`
	Status        string          `json:"status"`
	NextNodeID    *string         `json:"nextNodeId"`
	CreatedAt     string          `json:"createdAt"`
	StartedAt     *string         `json:"startedAt"`
	FinishedAt    *string         `json:"finishedAt"`
	TerminalError json.RawMessage `json:"terminalError"`
}

type NodeAttempt struct {
	ID                string          `json:"id"`
	DeviceExecutionID string          `json:"deviceExecutionId"`
	Sequence          int64           `json:"sequence"`
	NodeID            string          `json:"nodeId"`
	AttemptNumber     int             `json:"attemptNumber"`
	Status            string          `json:"status"`
	StartedAt         string          `json:"startedAt"`
	FinishedAt        *string         `json:"finishedAt"`
	Output            json.RawMessage `json:"output"`
	Error             json.RawMessage `json:"error"`
}

type AttemptPage struct {
	Attempts          []NodeAttempt `json:"attempts"`
	NextAfterSequence *int64        `json:"nextAfterSequence"`
}

// Repository is the storage-independent execution persistence boundary.
type Repository interface {
	LookupRequest(context.Context, string) (string, error)
	Admit(context.Context, Admission) (AdmissionResult, error)
	ActiveSnapshot(context.Context) (Snapshot, error)
	SnapshotByID(context.Context, string) (Snapshot, error)
	Attempts(context.Context, string, int64, int) (AttemptPage, error)
}
