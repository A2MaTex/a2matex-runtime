package editorgraph

import "encoding/json"

type ErrorPolicy string

const (
	ErrorPolicyStop        ErrorPolicy = "STOP"
	ErrorPolicyErrorBranch ErrorPolicy = "ERROR_BRANCH"
)

type JSONObject map[string]json.RawMessage

type Position struct {
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Label       *string `json:"label,omitempty"`
	Description *string `json:"description,omitempty"`
}

type RetryPolicy struct {
	MaxAttempts *int64 `json:"maxAttempts,omitempty"`
	DelayMS     *int64 `json:"delayMs,omitempty"`
}

type Node struct {
	TimeoutMS   *int64       `json:"timeoutMs,omitempty"`
	RetryPolicy *RetryPolicy `json:"retryPolicy,omitempty"`
	ID          string       `json:"id"`
	Type        string       `json:"type"`
	Config      JSONObject   `json:"config"`
	Bindings    JSONObject   `json:"bindings"`
	ErrorPolicy ErrorPolicy  `json:"errorPolicy"`
	Editor      Position     `json:"editor"`
}

type Edge struct {
	ID           string `json:"id"`
	SourceNodeID string `json:"sourceNodeId"`
	SourcePort   string `json:"sourcePort"`
	TargetNodeID string `json:"targetNodeId"`
}

type Graph struct {
	SchemaVersion int64  `json:"schemaVersion"`
	EntryNodeID   string `json:"entryNodeId"`
	Nodes         []Node `json:"nodes"`
	Edges         []Edge `json:"edges"`
}
