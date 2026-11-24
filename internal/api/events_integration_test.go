package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/api/events"
	"github.com/A2MaTex/a2matex-runtime/internal/api/runs"
	"github.com/A2MaTex/a2matex-runtime/internal/auth"
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/repositories"
	"github.com/A2MaTex/a2matex-runtime/internal/services/eventdelivery"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

func readEventFrame(t *testing.T, reader *bufio.Reader) execution.Event {
	t.Helper()
	fields := make(map[string]string)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal("incomplete SSE frame", err)
		}
		if line == "\n" {
			break
		}
		key, value, ok := strings.Cut(strings.TrimSuffix(line, "\n"), ": ")
		if !ok {
			t.Fatal("invalid frame line", line)
		}
		fields[key] = value
	}
	var event execution.Event
	if err := json.Unmarshal([]byte(fields["data"]), &event); err != nil || fields["id"] != event.Cursor || fields["event"] != event.Type {
		t.Fatal("inconsistent SSE projection", fields, err)
	}
	return event
}

func TestEventStreamRealSQLiteHTTPReplayAndLive(t *testing.T) {
	f := snapshotStore(t, 1)
	f.admit(t)
	appendEvents := func(count int) {
		t.Helper()
		pending := make([]execution.PendingEvent, count)
		for index := range pending {
			pending[index] = execution.PendingEvent{Type: constants.EventRunGroupStateChanged, OccurredAt: time.Now().UTC(), RunGroupID: f.admission.RunGroupID, Payload: json.RawMessage(`{"status":"RUNNING"}`)}
		}
		if err := f.repo.WithWriteTx(t.Context(), func(tx repositories.Tx) error {
			_, err := execution.AppendEventBatches(t.Context(), tx, pending)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	session, err := auth.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	router := newRouter(buildinfo.Info{}, session, nil, nil, runs.NewHandler(f.repo, nil, nil), nil, nil, events.NewHandler(eventdelivery.New(f.repo), logger), nil, logger)
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	snapshotRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/runs/"+f.admission.RunGroupID, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshotRequest.Header.Set("Authorization", "Bearer "+session.ConnectionToken())
	snapshotResponse, err := server.Client().Do(snapshotRequest)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot execution.Snapshot
	if err := json.NewDecoder(snapshotResponse.Body).Decode(&snapshot); err != nil || snapshotResponse.StatusCode != 200 || snapshot.Cursor != "2" {
		t.Fatal("invalid authoritative snapshot", snapshot, err)
	}
	if err := snapshotResponse.Body.Close(); err != nil {
		t.Fatal(err)
	}
	appendEvents(205) // These commits must be replayed after the snapshot cursor.
	open := func(cursor string) *http.Response {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/events", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+session.ConnectionToken())
		r.Header.Set("Accept", "text/event-stream")
		if cursor != "" {
			r.Header.Set("Last-Event-ID", cursor)
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := open("000" + snapshot.Cursor)
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" || response.Header.Get("X-Accel-Buffering") != "no" || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Content-Encoding") != "" || response.Header.Get("Content-Length") != "" {
		t.Fatal("incorrect real SSE headers", response.StatusCode, response.Header)
	}
	reader := bufio.NewReader(response.Body)
	if event := readEventFrame(t, reader); event.Cursor != "3" {
		t.Fatal(event)
	}
	appendEvents(1)
	before := f.counts(t)
	for cursor := 4; cursor <= 208; cursor++ {
		event := readEventFrame(t, reader)
		if event.Cursor != strconv.Itoa(cursor) || event.RunGroupID != f.admission.RunGroupID {
			t.Fatal("replay/live gap or foreign event", event)
		}
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if f.counts(t) != before {
		t.Fatal("event consumption wrote execution records")
	}
	// Omitted cursor captures the current watermark and follows only new commits.
	live := open("")
	defer live.Body.Close()
	appendEvents(1)
	if event := readEventFrame(t, bufio.NewReader(live.Body)); event.Cursor != "209" {
		t.Fatal("live stream replayed history", event)
	}
	if err := live.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.PruneEvents(t.Context(), 1, constants.EventPruneBatchSize); err != nil {
		t.Fatal(err)
	}
	expired := open("2")
	defer expired.Body.Close()
	var body map[string]map[string]any
	if err := json.NewDecoder(expired.Body).Decode(&body); err != nil || expired.StatusCode != 410 || body["error"]["code"] != "EVENT_CURSOR_EXPIRED" {
		t.Fatal("expiry was not a pre-stream JSON response", expired.StatusCode, body, err)
	}
}
