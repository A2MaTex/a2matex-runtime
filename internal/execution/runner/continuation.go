package runner

import (
	"context"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"time"
)

// Continuation is device-owned live state with no graph/runner reference.
// Only its joined owner may inspect, resume, or clear it.
type Continuation struct {
	initialized bool
	Result      Result
	Values      bindings.Values
	Visited     map[string]bool
	Number      int
	PreviousID  string
	Expected    string
	RetryDue    time.Time
}

func (s *Continuation) Resume() { s.Expected = "QUEUED" }
func (s *Continuation) Close() {
	for _, v := range s.Values.WorkflowInputs {
		clear(v)
	}
	clear(s.Values.WorkflowInputs)
	for _, out := range s.Values.CommittedOutputs {
		for _, v := range out {
			clear(v)
		}
		clear(out)
	}
	clear(s.Values.CommittedOutputs)
	clear(s.Visited)
	*s = Continuation{}
}
func waitRetry(ctx context.Context, park <-chan struct{}, due time.Time) error {
	timer := time.NewTimer(max(time.Until(due), 0))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-park:
		return xerror.ErrProductRunParked
	case <-timer.C:
		return nil
	}
}

// Initialize gives queued members owned values before any graph epoch can park.
func (s *Continuation) Initialize(ctx context.Context, request Request, entry string) error {
	if s.initialized {
		return nil
	}
	inputs, err := copyInputs(ctx, request)
	if err != nil {
		return err
	}
	s.Values = bindings.Values{WorkflowInputs: inputs, CommittedOutputs: make(map[string]ir.Object), Variables: request.Variables}
	s.Visited = make(map[string]bool)
	s.Result.NextNodeID = entry
	s.Number, s.Expected = 1, constants.ExecutionQueued
	s.initialized = true
	return nil
}
