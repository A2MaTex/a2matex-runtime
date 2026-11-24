package events

import "github.com/A2MaTex/a2matex-runtime/internal/execution"

// StreamRequest is decoded from headers, never from a JSON body.
type StreamRequest struct {
	After *int64
}

// StreamEvent is the existing A6 payload carried by each SSE data line.
type StreamEvent = execution.Event
