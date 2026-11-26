package api

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/api/artifacts"
	attemptapi "github.com/A2MaTex/a2matex-runtime/internal/api/attempts"
	"github.com/A2MaTex/a2matex-runtime/internal/api/runs"
	"github.com/A2MaTex/a2matex-runtime/internal/auth"
	"github.com/A2MaTex/a2matex-runtime/internal/buildinfo"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/invocation"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/outputs"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/runner"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/delay"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/end"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/screenshot"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/start"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/editorgraph"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
	validationservice "github.com/A2MaTex/a2matex-runtime/internal/validation/service"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	"github.com/google/uuid"
)

type artifactCapture func(context.Context, string) ([]byte, error)

func (f artifactCapture) CaptureScreenshot(ctx context.Context, serial string) ([]byte, error) {
	return f(ctx, serial)
}

func TestArtifactDownloadOfG19CommittedPNG(t *testing.T) {
	for _, mode := range []string{"FULL_SCREEN", "SCREEN_PERCENT"} {
		t.Run(mode, func(t *testing.T) {
			f := snapshotStore(t, 1)
			logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
			storage := services.NewArtifactService(f.store, f.repo, logger)
			if err := storage.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := storage.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			})
			if err := f.checkpoints.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.checkpoints = services.NewCheckpointService(f.repo, storage)
			session, err := auth.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			f.token = session.ConnectionToken()
			f.router = newRouter(buildinfo.Info{}, session, nil, nil, runs.NewHandler(f.repo, nil, nil), attemptapi.NewHandler(f.repo), artifacts.NewHandler(storage, logger), nil, nil, logger, nil)
			get := func(id string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+id, nil)
				req.Header.Set("Authorization", "Bearer "+f.token)
				rr := httptest.NewRecorder()
				f.router.ServeHTTP(rr, req)
				return rr
			}
			im := image.NewNRGBA(image.Rect(0, 0, 7, 5))
			im.SetNRGBA(1, 1, color.NRGBA{R: 255, A: 255})
			var encoded bytes.Buffer
			if err := png.Encode(&encoded, im); err != nil {
				t.Fatal(err)
			}
			stage, err := storage.Stage(t.Context(), uuid.NewString(), encoded.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if rr := get(stage.ID); rr.Code != 404 {
				t.Fatal("uncommitted file was downloadable", rr.Code)
			}
			catalog, err := nodecontract.NewDefaultRegistry()
			if err != nil {
				t.Fatal(err)
			}
			captures := 0
			handlers, err := registry.New(catalog, []registry.Registration{
				{Type: "flow.start", RuntimeHandler: "flow.start", Run: start.Invoke},
				{Type: "flow.delay", RuntimeHandler: "flow.delay", Run: delay.Invoke},
				{Type: "flow.end", RuntimeHandler: "flow.end", Run: end.Invoke},
				{Type: constants.ScreenshotNodeType, RuntimeHandler: constants.ScreenshotNodeType, Run: screenshot.NewScreenshot(artifactCapture(func(context.Context, string) ([]byte, error) { captures++; return encoded.Bytes(), nil })).Invoke},
			})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile("../../examples/g19/compile-screenshot.json")
			if err != nil {
				t.Fatal(err)
			}
			var example struct {
				EditorGraph editorgraph.Graph `json:"editorGraph"`
				InputSchema json.RawMessage   `json:"inputSchema"`
			}
			if err := json.Unmarshal(raw, &example); err != nil {
				t.Fatal(err)
			}
			example.EditorGraph.Nodes[1].Bindings["durationMs"] = []byte(`{"kind":"LITERAL","value":0}`)
			example.EditorGraph.Nodes[2].Config["targetMode"], err = json.Marshal(mode)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "SCREEN_PERCENT" {
				example.EditorGraph.Nodes[2].Bindings = ir.Object{"startXPercent": []byte(`{"kind":"LITERAL","value":20}`), "startYPercent": []byte(`{"kind":"LITERAL","value":20}`), "endXPercent": []byte(`{"kind":"LITERAL","value":40}`), "endYPercent": []byte(`{"kind":"LITERAL","value":30}`)}
			}
			graph, err := json.Marshal(example.EditorGraph)
			if err != nil {
				t.Fatal(err)
			}
			compiled, err := validationservice.New(catalog).Compile(graph, example.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			validator, err := runtimeir.New(catalog, handlers, "0.1.0")
			if err != nil {
				t.Fatal(err)
			}
			checked, err := validator.Validate(t.Context(), compiled.RuntimeIR, []json.RawMessage{[]byte(`{}`)})
			if err != nil || checked.Validation.Status != "VALID" {
				t.Fatal(checked, err)
			}
			resolver, err := bindings.New(catalog)
			if err != nil {
				t.Fatal(err)
			}
			invoker, err := invocation.New(handlers)
			if err != nil {
				t.Fatal(err)
			}
			output, err := outputs.New(catalog)
			if err != nil {
				t.Fatal(err)
			}
			worker, err := runner.New(t.Context(), checked.Document, runner.Dependencies{Bindings: resolver, Invoker: invoker, Outputs: output, Checkpoints: f.checkpoints, Artifacts: storage})
			if err != nil {
				t.Fatal(err)
			}
			f.admit(t)
			device := f.admission.Devices[0]
			result, err := worker.Run(t.Context(), runner.Request{RunGroupID: f.admission.RunGroupID, DeviceExecutionID: device.DeviceExecutionID, ADBSerial: device.ADBSerial, WorkflowInputs: ir.Object{}})
			if err != nil || result.DeviceStatus != constants.ExecutionSucceeded {
				t.Fatal(result, err)
			}
			page := historyPage(t, f, device.DeviceExecutionID, "", 200)
			if len(page.Attempts) != 4 {
				t.Fatal(page)
			}
			var outputID struct {
				ArtifactID string `json:"artifactId"`
			}
			if err := json.Unmarshal(page.Attempts[2].Output, &outputID); err != nil {
				t.Fatal(err)
			}
			a, err := f.repo.ArtifactByID(t.Context(), outputID.ArtifactID)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.store.StateDirectory(), "artifacts", a.FileKey)
			stored, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			before, err := f.repo.SnapshotByID(t.Context(), f.admission.RunGroupID)
			if err != nil {
				t.Fatal(err)
			}
			responses := make(chan *httptest.ResponseRecorder, 4)
			for range 4 {
				go func() { responses <- get(outputID.ArtifactID) }()
			}
			for range 4 {
				rr := <-responses
				if rr.Code != 200 || !bytes.Equal(rr.Body.Bytes(), stored) || rr.Header().Get("Content-Type") != "image/png" || rr.Header().Get("Cache-Control") != "no-store" {
					t.Fatal(rr.Code, rr.Body.String())
				}
				decoded, err := png.Decode(bytes.NewReader(rr.Body.Bytes()))
				if err != nil || decoded.Bounds().Dx() != a.Width || decoded.Bounds().Dy() != a.Height {
					t.Fatal(decoded, err)
				}
			}
			if captures != 1 {
				t.Fatal("download recaptured image", captures)
			}
			after, err := f.repo.SnapshotByID(t.Context(), f.admission.RunGroupID)
			if err != nil || after.Cursor != before.Cursor {
				t.Fatal("download mutated execution", before, after, err)
			}
			backup := path + ".saved"
			if err := os.Rename(path, backup); err != nil {
				t.Fatal(err)
			}
			if rr := get(outputID.ArtifactID); rr.Code != 404 {
				t.Fatal("missing content", rr.Code, rr.Body.String())
			}
			if err := os.Rename(backup, path); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(path, 0); err != nil {
				t.Fatal(err)
			}
			if rr := get(outputID.ArtifactID); rr.Code != 500 || !bytes.Contains(rr.Body.Bytes(), []byte(constants.PersistenceFailed)) {
				t.Fatal("length mismatch", rr.Code, rr.Body.String())
			}
			if err := os.WriteFile(path, stored, 0600); err != nil {
				t.Fatal(err)
			}
			if rr := get(outputID.ArtifactID); rr.Code != 200 || !bytes.Equal(rr.Body.Bytes(), stored) {
				t.Fatal(rr.Code)
			}
		})
	}
}
