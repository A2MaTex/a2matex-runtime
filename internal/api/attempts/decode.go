package attempts

import (
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

func decodeRequest(id string, r *http.Request) (PageQuery, bool) {
	query := PageQuery{Limit: constants.MaxAttemptPageSize}
	if !execution.ValidID(id) || r.ContentLength > 0 {
		return query, false
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return query, false
	}
	for key, entries := range values {
		if (key != "afterSequence" && key != "limit") || len(entries) != 1 {
			return query, false
		}
		value, ok := decimal(entries[0])
		if !ok {
			return query, false
		}
		if key == "afterSequence" {
			query.AfterSequence = value
			continue
		}
		if value < 1 || value > constants.MaxAttemptPageSize {
			return query, false
		}
		query.Limit = int(value)
	}
	if r.Body != nil && r.Body != http.NoBody {
		var first [1]byte
		if _, err := io.ReadFull(r.Body, first[:]); err != io.EOF {
			return query, false
		}
	}
	return query, true
}

func decimal(value string) (int64, bool) {
	if value == "" {
		return 0, false
	}
	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	return n, err == nil
}
