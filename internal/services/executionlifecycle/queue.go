package executionlifecycle

import (
	"slices"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// deviceQueue is protected by the lifecycle mutex; it performs no I/O or dispatch.
type deviceQueue struct {
	members []string
	next    int
	active  map[string]bool
	stopped bool
}

func newDeviceQueue(members []string) (*deviceQueue, error) {
	invalid := func() (*deviceQueue, error) {
		return nil, lifecycleError("configure_device_queue", xerror.ErrExecutionLifecycleInvalid)
	}
	if len(members) == 0 {
		return invalid()
	}
	seen := make(map[string]bool, len(members))
	for _, id := range members {
		if !execution.ValidID(id) || seen[id] {
			return invalid()
		}
		seen[id] = true
	}
	// Every admitted member can hold one reservation; no separate capacity setting.
	return &deviceQueue{members: slices.Clone(members), active: make(map[string]bool, len(members))}, nil
}

// take returns an index into the original owned device order, reserving it once.
func (q *deviceQueue) take() (int, bool) {
	if q.stopped || q.next == len(q.members) {
		return 0, false
	}
	index := q.next
	q.active[q.members[index]] = true
	q.next++
	return index, true
}

func (q *deviceQueue) release(memberID string) error {
	if !q.active[memberID] {
		return lifecycleError("release_device_slot", xerror.ErrExecutionStaleState)
	}
	delete(q.active, memberID)
	return nil
}

func (q *deviceQueue) stop() { q.stopped = true }
