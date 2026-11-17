// Package ir defines the executable workflow document shared by compilation and execution.
package ir

import "encoding/json"

type Object map[string]json.RawMessage

type Document struct {
	FormatVersion         int             `json:"formatVersion"`
	MinimumRuntimeVersion string          `json:"minimumRuntimeVersion"`
	Metadata              Metadata        `json:"metadata"`
	EntryNodeID           string          `json:"entryNodeId"`
	InputSchema           json.RawMessage `json:"inputSchema"`
	Nodes                 []Node          `json:"nodes"`
	Edges                 []Edge          `json:"edges"`
	RequiredCapabilities  []string        `json:"requiredCapabilities"`
}

type Metadata struct {
	SourceGraphSchemaVersion int64 `json:"sourceGraphSchemaVersion"`
}

type Node struct {
	ID             string      `json:"id"`
	Type           string      `json:"type"`
	RuntimeHandler string      `json:"runtimeHandler"`
	Config         Object      `json:"config"`
	Inputs         Object      `json:"inputs"`
	TimeoutMS      int64       `json:"timeoutMs"`
	RetryPolicy    RetryPolicy `json:"retryPolicy"`
	ErrorPolicy    string      `json:"errorPolicy"`
}

type RetryPolicy struct {
	MaxAttempts int64 `json:"maxAttempts"`
	DelayMS     int64 `json:"delayMs"`
}

type Edge struct {
	ID           string `json:"id"`
	SourceNodeID string `json:"sourceNodeId"`
	SourcePort   string `json:"sourcePort"`
	TargetNodeID string `json:"targetNodeId"`
}
