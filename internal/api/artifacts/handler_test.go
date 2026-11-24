package artifacts_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/api"
	"github.com/A2MaTex/a2matex-runtime/internal/api/artifacts"
	apiresponse "github.com/A2MaTex/a2matex-runtime/internal/api/response"
	"github.com/A2MaTex/a2matex-runtime/internal/auth"
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const artifactID = "a3526b0c-b066-43d9-a681-5630b4469e87"

type readerFunc func(context.Context, string) (execution.Artifact, *os.File, error)

func (f readerFunc) Open(ctx context.Context, id string) (execution.Artifact, *os.File, error) {
	return f(ctx, id)
}

type logBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}
func (b *logBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.data.String() }

func metadata(data []byte) execution.Artifact {
	digest := sha256.Sum256(data)
	return execution.Artifact{ID: artifactID, AttemptID: "f4140c2f-c8cc-431d-8a92-3ea07cb2403f", FileKey: execution.ArtifactFileKey(artifactID),
		MediaType: constants.ScreenshotMediaType, ByteLength: int64(len(data)), Width: 1, Height: 1, Digest: hex.EncodeToString(digest[:]), CreatedAt: time.Now().UnixMilli()}
}

func testRouter(t *testing.T, reader artifacts.Reader, logs io.Writer) (http.Handler, string) {
	t.Helper()
	session, err := auth.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := registry.New(catalog, nil)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	return api.NewHandler(buildinfo.Info{}, session, nil, nil, nil, nil, artifacts.NewHandler(reader, logger), nil, handlers, logger), session.ConnectionToken()
}

func fixtureFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private-image.png")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDownloadRequestAndErrorContracts(t *testing.T) {
	for _, name := range []string{"success", "range and conditions", "no token", "foreign token", "duplicate token", "bad id", "uppercase id", "wrong version", "query", "body", "chunked body", "empty chunked", "trailing slash", "extra segment", "traversal", "HEAD", "POST", "missing", "persistence", "unexpected", "nil handle", "invalid metadata", "wrong metadata id", "too large", "empty source", "closed source", "error with handle"} {
		t.Run(name, func(t *testing.T) {
			data := []byte("private PNG fixture bytes")
			path := fixtureFile(t, data)
			if name == "empty source" {
				if err := os.Truncate(path, 0); err != nil {
					t.Fatal(err)
				}
			}
			var file *os.File
			calls := 0
			reader := readerFunc(func(ctx context.Context, id string) (execution.Artifact, *os.File, error) {
				calls++
				a := metadata(data)
				switch name {
				case "missing":
					return execution.Artifact{}, nil, xerror.ErrExecutionNotFound
				case "persistence":
					return execution.Artifact{}, nil, fmt.Errorf("private storage text: %w", xerror.ErrExecutionPersistence)
				case "unexpected":
					return execution.Artifact{}, nil, errors.New("private reader error")
				case "nil handle":
					return a, nil, nil
				case "invalid metadata":
					a.Width = 0
				case "wrong metadata id":
					a.ID = "f4140c2f-c8cc-431d-8a92-3ea07cb2403f"
					a.FileKey = execution.ArtifactFileKey(a.ID)
				case "too large":
					a.ByteLength = constants.MaxScreenshotBytes + 1
				}
				var err error
				file, err = os.Open(path)
				if err != nil {
					return execution.Artifact{}, nil, err
				}
				if name == "closed source" {
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if name == "error with handle" {
					return a, file, xerror.ErrExecutionNotFound
				}
				return a, file, nil
			})
			logs := &logBuffer{}
			router, token := testRouter(t, reader, logs)
			url, method, status, wantCalls := "/api/v1/artifacts/"+artifactID, http.MethodGet, http.StatusOK, 1
			code := ""
			var body io.Reader
			switch name {
			case "no token", "foreign token", "duplicate token":
				url = "/api/v1/artifacts/not-an-id"
				status, wantCalls, code = 401, 0, constants.APIErrorCodeUnauthorized
			case "bad id":
				url = "/api/v1/artifacts/not-an-id"
				status, wantCalls, code = 400, 0, constants.APIErrorCodeInvalidRequest
			case "uppercase id":
				url = "/api/v1/artifacts/" + strings.ToUpper(artifactID)
				status, wantCalls, code = 400, 0, constants.APIErrorCodeInvalidRequest
			case "wrong version":
				url = "/api/v1/artifacts/a3526b0c-b066-13d9-a681-5630b4469e87"
				status, wantCalls, code = 400, 0, constants.APIErrorCodeInvalidRequest
			case "query":
				url += "?path=private-path"
				status, wantCalls, code = 400, 0, constants.APIErrorCodeInvalidRequest
			case "body", "chunked body":
				body = strings.NewReader("x")
				status, wantCalls, code = 400, 0, constants.APIErrorCodeInvalidRequest
			case "empty chunked":
				body = strings.NewReader("")
			case "trailing slash":
				url += "/"
				status, wantCalls, code = 404, 0, constants.APIErrorCodeResourceNotFound
			case "extra segment":
				url += "/private-path"
				status, wantCalls, code = 404, 0, constants.APIErrorCodeResourceNotFound
			case "traversal":
				url = "/api/v1/artifacts/%2e%2e%2fprivate-path"
				status, wantCalls, code = 404, 0, constants.APIErrorCodeResourceNotFound
			case "HEAD", "POST":
				method = name
				status, wantCalls, code = 404, 0, constants.APIErrorCodeResourceNotFound
			case "missing", "error with handle":
				status, code = 404, constants.APIErrorCodeResourceNotFound
			case "persistence", "empty source", "closed source":
				status, code = 500, constants.PersistenceFailed
			case "unexpected", "nil handle", "invalid metadata", "wrong metadata id", "too large":
				status, code = 500, constants.InternalError
			}
			req := httptest.NewRequest(method, url, body)
			if name != "no token" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			if name == "foreign token" {
				req.Header.Set("Authorization", "Bearer private-foreign-token")
			}
			if name == "duplicate token" {
				req.Header.Add("Authorization", "Bearer "+token)
			}
			if name == "chunked body" || name == "empty chunked" {
				req.ContentLength = -1
			}
			if name == "range and conditions" {
				req.Header.Set("Range", "bytes=0-2")
				req.Header.Set("If-Range", "private-validator")
				req.Header.Set("If-None-Match", "*")
			}
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != status || calls != wantCalls || rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(rr.Code, rr.Body.String(), calls)
			}
			if code != "" {
				var envelope struct {
					Error apiresponse.Error `json:"error"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != code || envelope.Error.Retryable {
					t.Fatal(rr.Body.String(), err)
				}
				if !strings.HasPrefix(rr.Header().Get("Content-Type"), "application/json") || rr.Header().Get("Content-Length") != "" {
					t.Fatal("stale PNG headers", rr.Header())
				}
				if !strings.Contains(logs.String(), `"level":"ERROR"`) {
					t.Fatal(logs.String())
				}
			} else if !bytes.Equal(rr.Body.Bytes(), data) || rr.Header().Get("Content-Type") != "image/png" || rr.Header().Get("Content-Length") != fmt.Sprint(len(data)) {
				t.Fatal(rr.Body.String(), rr.Header())
			}
			for _, key := range []string{"Content-Disposition", "ETag", "Last-Modified", "Content-Range", "Accept-Ranges"} {
				if rr.Header().Get(key) != "" {
					t.Fatal(key, rr.Header())
				}
			}
			if file != nil {
				if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatal("handle not closed", err)
				}
			}
			if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), "private") || strings.Contains(logs.String(), artifactID) || strings.Contains(rr.Body.String(), "private") && code != "" {
				t.Fatal("sensitive diagnostic", logs.String(), rr.Body.String())
			}
			if strings.Count(logs.String(), "\n") != 1 {
				t.Fatal("duplicated log", logs.String())
			}
		})
	}
}

type faultWriter struct {
	*httptest.ResponseRecorder
	mode   string
	cancel context.CancelFunc
	file   **os.File
	offset int64
}

func (w *faultWriter) Write(data []byte) (int, error) {
	if w.mode == "write error" {
		return 0, errors.New("private socket failure")
	}
	if w.mode == "panic" {
		_, _ = w.ResponseRecorder.Write(data[:8])
		panic("private panic value")
	}
	n, err := w.ResponseRecorder.Write(data)
	if w.mode == "cancel" {
		w.offset, _ = (*w.file).Seek(0, io.SeekCurrent)
		w.cancel()
	}
	return n, err
}

func TestTransferFailureAbortsWithoutJSONAndClosesHandle(t *testing.T) {
	for _, name := range []string{"write error", "cancel", "panic", "cancel before open", "cancel after open"} {
		t.Run(name, func(t *testing.T) {
			data := bytes.Repeat([]byte("private PNG bytes "), 16384)
			path := fixtureFile(t, data)
			var file *os.File
			calls := 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			logs := &logBuffer{}
			router, token := testRouter(t, readerFunc(func(context.Context, string) (execution.Artifact, *os.File, error) {
				calls++
				var err error
				file, err = os.Open(path)
				if name == "cancel after open" {
					cancel()
				}
				return metadata(data), file, err
			}), logs)
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/artifacts/"+artifactID, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			if name == "cancel before open" {
				cancel()
			}
			writer := &faultWriter{ResponseRecorder: httptest.NewRecorder(), mode: name, cancel: cancel, file: &file}
			var caught any
			func() { defer func() { caught = recover() }(); router.ServeHTTP(writer, req) }()
			if caught != http.ErrAbortHandler || strings.Contains(writer.Body.String(), `{"error"`) {
				t.Fatal(caught, writer.Body.String())
			}
			if name == "cancel before open" {
				if calls != 0 {
					t.Fatal(calls)
				}
			} else if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatal("handle leaked", err)
			}
			if name == "cancel" && (writer.offset <= 0 || writer.offset >= int64(len(data))) {
				t.Fatal("copy read entire image before cancellation", writer.offset)
			}
			if strings.Count(logs.String(), "\n") != 1 || !strings.Contains(logs.String(), `"level":"ERROR"`) || strings.Contains(logs.String(), "private") || strings.Contains(logs.String(), token) {
				t.Fatal(logs.String())
			}
			if name == "panic" {
				if !strings.Contains(logs.String(), "local_api_panic_recovered") {
					t.Fatal(logs.String())
				}
			} else {
				if !strings.Contains(logs.String(), "artifact_transfer_aborted") || strings.Contains(logs.String(), "local_api_panic_recovered") {
					t.Fatal(logs.String())
				}
				status := 200
				if name == "cancel before open" || name == "cancel after open" {
					status = 0
				}
				if !strings.Contains(logs.String(), fmt.Sprintf(`"status":%d`, status)) {
					t.Fatal(logs.String())
				}
			}
		})
	}
}

func TestRealHTTPShortBodyAndPanicAbortKeepServerAlive(t *testing.T) {
	for _, name := range []string{"short source", "writer panic"} {
		t.Run(name, func(t *testing.T) {
			data := bytes.Repeat([]byte("x"), 65536)
			path := fixtureFile(t, data)
			var file *os.File
			logs := &logBuffer{}
			router, token := testRouter(t, readerFunc(func(context.Context, string) (execution.Artifact, *os.File, error) {
				var err error
				file, err = os.Open(path)
				a := metadata(data)
				a.ByteLength *= 2
				return a, file, err
			}), logs)
			done := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/api/v1/artifacts/") {
					defer close(done)
					if name == "writer panic" {
						w = &panicHTTPWriter{ResponseWriter: w}
					}
				}
				router.ServeHTTP(w, r)
			}))
			defer server.Close()
			client := server.Client()
			client.Timeout = 3 * time.Second
			req, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/artifacts/"+artifactID, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if response.StatusCode != 200 || readErr == nil || closeErr != nil || len(body) == 0 || bytes.Contains(body, []byte(`{"error"`)) {
				t.Fatal(response.StatusCode, len(body), readErr, closeErr)
			}
			<-done
			if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatal("handle leaked", err)
			}
			if strings.Count(logs.String(), "\n") != 1 || strings.Contains(logs.String(), "private") {
				t.Fatal(logs.String())
			}
			ready, err := client.Get(server.URL + "/api/v1/health")
			if err != nil {
				t.Fatal(err)
			}
			if ready.StatusCode != 200 {
				t.Fatal(ready.StatusCode)
			}
			if err := ready.Body.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type panicHTTPWriter struct{ http.ResponseWriter }

func (w *panicHTTPWriter) Write(data []byte) (int, error) {
	if _, err := w.ResponseWriter.Write(data); err != nil {
		return 0, err
	}
	panic("private stream panic")
}

func TestRealHTTPClientCancellationReleasesHandle(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 8*1024*1024)
	path := fixtureFile(t, data)
	var file *os.File
	logs := &logBuffer{}
	router, token := testRouter(t, readerFunc(func(context.Context, string) (execution.Artifact, *os.File, error) {
		var err error
		file, err = os.Open(path)
		return metadata(data), file, err
	}), logs)
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		router.ServeHTTP(&waitForClientCancel{ResponseWriter: w, ctx: r.Context()}, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/artifacts/"+artifactID, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled transfer did not exit")
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("handle leaked", err)
	}
	if strings.Count(logs.String(), "\n") != 1 || !strings.Contains(logs.String(), "artifact_transfer_cancelled") || strings.Contains(logs.String(), token) {
		t.Fatal(logs.String())
	}
}

type waitForClientCancel struct {
	http.ResponseWriter
	ctx context.Context
}

func (w *waitForClientCancel) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if err == nil {
		<-w.ctx.Done()
	}
	return n, err
}
