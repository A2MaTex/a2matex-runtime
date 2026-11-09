package repositories

import (
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	"sync"
)

// ExecutionRepository implements execution persistence using the supervised SQLite store.
type ExecutionRepository struct {
	store       *sqlitestore.Store
	eventMu     sync.Mutex
	eventWakeup chan struct{}
	maintenance chan struct{}
}

func NewExecutionRepository(store *sqlitestore.Store) *ExecutionRepository {
	return &ExecutionRepository{store: store, eventWakeup: make(chan struct{}), maintenance: make(chan struct{}, 1)}
}

var _ execution.Repository = (*ExecutionRepository)(nil)
var _ execution.EventReader = (*ExecutionRepository)(nil)

// EventWakeup registers for the next committed append. Register before querying
// and reacquire the channel after each wakeup. Signals may coalesce.
func (s *ExecutionRepository) EventWakeup() <-chan struct{} {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	return s.eventWakeup
}

func (s *ExecutionRepository) EventMaintenance() <-chan struct{} { return s.maintenance }

func (s *ExecutionRepository) notifyEvents() {
	s.eventMu.Lock()
	close(s.eventWakeup)
	s.eventWakeup = make(chan struct{})
	s.eventMu.Unlock()
	select {
	case s.maintenance <- struct{}{}:
	default:
	}
}
