package attempts

import "github.com/A2MaTex/a2matex-runtime/internal/execution"

// PageResponse preserves the existing A5/A7 attempt projection and null fields.
type PageResponse = execution.AttemptPage

// PageQuery contains validated sequence pagination, not an event replay cursor.
type PageQuery struct {
	AfterSequence int64
	Limit         int
}
