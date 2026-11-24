package events

import (
	"io"
	"net/http"
	"strings"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
)

func decodeRequest(r *http.Request) (StreamRequest, bool) {
	var request StreamRequest
	accept := r.Header.Values("Accept")
	if len(accept) != 1 || !strings.EqualFold(strings.Trim(accept[0], " \t"), "text/event-stream") ||
		r.URL.RawQuery != "" || r.ContentLength > 0 {
		return request, false
	}
	if cursors := r.Header.Values("Last-Event-ID"); len(cursors) != 0 {
		if len(cursors) != 1 {
			return request, false
		}
		cursor, err := execution.ParseEventCursor(cursors[0])
		if err != nil {
			return request, false
		}
		request.After = &cursor
	}
	if r.Body != nil && r.Body != http.NoBody {
		var first [1]byte
		if _, err := io.ReadFull(r.Body, first[:]); err != io.EOF {
			return request, false
		}
	}
	return request, true
}
