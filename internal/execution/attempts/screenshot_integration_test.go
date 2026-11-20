package attempts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/screenshot"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/artifactstore"
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type screenshotCapture func(context.Context, string) ([]byte, error)

func (f screenshotCapture) CaptureScreenshot(ctx context.Context, serial string) ([]byte, error) {
	return f(ctx, serial)
}

type observedArtifacts struct {
	*services.ArtifactService
	afterStage func()
	discards   int
}

func (s *observedArtifacts) Stage(ctx context.Context, id string, data []byte) (execution.Artifact, error) {
	a, err := s.ArtifactService.Stage(ctx, id, data)
	if err == nil && s.afterStage != nil {
		s.afterStage()
	}
	return a, err
}
func (s *observedArtifacts) DiscardUncommitted(ctx context.Context, id string) {
	s.discards++
	s.ArtifactService.DiscardUncommitted(ctx, id)
}

func screenshotPNG(t *testing.T) ([]byte, *image.NRGBA) {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 7, 5))
	for y := range 5 {
		for x := range 7 {
			im.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 30), G: uint8(y * 40), B: 100, A: 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, im); err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), im
}

func TestScreenshotRealHandlerAndSQLiteSettlement(t *testing.T) {
	for _, name := range []string{"full", "pixels", "percent", "outside frame", "invalid PNG", "intentional", "control only", "owner loss", "rollback", "confirmed readback", "uncertain rollback"} {
		t.Run(name, func(t *testing.T) {
			r := screenshotRequest()
			frame, original := screenshotPNG(t)
			bounds := original.Bounds()
			switch name {
			case "pixels":
				r.Node.Config["targetMode"] = []byte(`"ABSOLUTE_PIXELS"`)
				r.Node.Inputs = ir.Object{"startX": []byte(`{"source":"LITERAL","value":1}`), "startY": []byte(`{"source":"LITERAL","value":1}`), "endX": []byte(`{"source":"LITERAL","value":3}`), "endY": []byte(`{"source":"LITERAL","value":2}`)}
				bounds = image.Rect(1, 1, 3, 2)
			case "percent":
				r.Node.Config["targetMode"] = []byte(`"SCREEN_PERCENT"`)
				r.Node.Inputs = ir.Object{"startXPercent": []byte(`{"source":"LITERAL","value":20}`), "startYPercent": []byte(`{"source":"LITERAL","value":20}`), "endXPercent": []byte(`{"source":"LITERAL","value":40}`), "endYPercent": []byte(`{"source":"LITERAL","value":30}`)}
				bounds = image.Rect(1, 1, 3, 2)
			case "outside frame":
				r.Node.Config["targetMode"] = []byte(`"ABSOLUTE_PIXELS"`)
				r.Node.Inputs = ir.Object{"startX": []byte(`{"source":"LITERAL","value":0}`), "startY": []byte(`{"source":"LITERAL","value":0}`), "endX": []byte(`{"source":"LITERAL","value":4096}`), "endY": []byte(`{"source":"LITERAL","value":4096}`)}
			case "invalid PNG":
				frame = []byte("not a PNG")
			}
			repo, _, db := storeFixture(t, r)
			artifacts := &observedArtifacts{ArtifactService: services.NewArtifactService(db, repo, slog.New(slog.NewTextHandler(io.Discard, nil)))}
			if err := artifacts.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := artifacts.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			})
			var cp Checkpoints = services.NewCheckpointService(repo, artifacts)
			fault := &uncertainVariableRepository{ExecutionRepository: repo, commit: name == "confirmed readback"}
			if name == "confirmed readback" || name == "uncertain rollback" {
				cp = services.NewCheckpointService(fault, artifacts)
			}
			if name == "rollback" {
				if _, err := db.DB().ExecContext(t.Context(), `CREATE TRIGGER reject_screenshot BEFORE INSERT ON artifacts BEGIN SELECT RAISE(ABORT, 'controlled artifact rollback'); END`); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			accepted := false
			r.Control = func() Control {
				return Control{GroupCancellation: accepted, ShutdownDeadline: time.Now().Add(time.Second)}
			}
			if name == "intentional" || name == "control only" || name == "owner loss" {
				artifacts.afterStage = func() {
					accepted = name != "owner loss"
					if name != "control only" {
						cancel()
					}
				}
			}
			captures := 0
			handler := screenshot.NewScreenshot(screenshotCapture(func(_ context.Context, serial string) ([]byte, error) {
				captures++
				if serial != r.ADBSerial {
					t.Error("wrong device")
				}
				rows := storedAttempts(t, repo, r)
				if len(rows) != 1 || rows[0].Status != constants.ExecutionRunning {
					t.Fatal("capture before durable begin", rows)
				}
				return frame, nil
			})).Invoke
			deps := dependencies(t, r, handler, cp)
			deps.Artifacts = artifacts
			got, err := coordinator(t, deps).Run(ctx, r)
			if captures != 1 {
				t.Fatal("capture count", captures)
			}
			rows := storedAttempts(t, repo, r)
			var count int
			if e := db.DB().QueryRowContext(t.Context(), "SELECT count(*) FROM artifacts").Scan(&count); e != nil {
				t.Fatal(e)
			}
			files, e := filepath.Glob(filepath.Join(db.StateDirectory(), "artifacts", "*.png"))
			if e != nil {
				t.Fatal(e)
			}
			success := name == "full" || name == "pixels" || name == "percent" || name == "confirmed readback"
			if success {
				if err != nil || !got.Finished || got.Outcome.Status != constants.ExecutionSucceeded || count != 1 || len(files) != 1 || artifacts.discards != 0 {
					t.Fatal(got, err, count, files, artifacts.discards)
				}
				var out map[string]string
				if e := json.Unmarshal(got.Outcome.Output, &out); e != nil {
					t.Fatal(e)
				}
				a, e := repo.ArtifactByID(t.Context(), out["artifactId"])
				if e != nil {
					t.Fatal(e)
				}
				data, e := os.ReadFile(files[0])
				if e != nil {
					t.Fatal(e)
				}
				digest := sha256.Sum256(data)
				if !a.Valid() || a.AttemptID != r.Ref.AttemptID || a.Width != bounds.Dx() || a.Height != bounds.Dy() || a.ByteLength != int64(len(data)) || a.Digest != hex.EncodeToString(digest[:]) {
					t.Fatal(a)
				}
				decoded, e := png.Decode(bytes.NewReader(data))
				if e != nil {
					t.Fatal(e)
				}
				for y := range bounds.Dy() {
					for x := range bounds.Dx() {
						if color.NRGBAModel.Convert(decoded.At(x, y)) != color.NRGBAModel.Convert(original.At(x+bounds.Min.X, y+bounds.Min.Y)) {
							t.Fatal("wrong cropped pixel", x, y)
						}
					}
				}
				assertStoredResult(t, repo, r, got)
			} else {
				if count != 0 {
					t.Fatal("failed attempt has artifact row")
				}
				wantStatus := constants.ExecutionFailed
				switch name {
				case "owner loss", "rollback", "uncertain rollback":
					wantStatus = constants.ExecutionRunning
					if err == nil || got.Finished {
						t.Fatal(got, err)
					}
				case "intentional", "control only":
					wantStatus = constants.ExecutionCancelled
				}
				if rows[0].Status != wantStatus {
					t.Fatal(rows, got, err)
				}
				if wantStatus != constants.ExecutionRunning && (err != nil || !got.Finished) {
					t.Fatal(got, err)
				}
				wantFiles, wantDiscards := 0, 1
				if name == "outside frame" || name == "invalid PNG" {
					wantDiscards = 0
				}
				if name == "uncertain rollback" {
					wantFiles, wantDiscards = 1, 0
				}
				if len(files) != wantFiles || artifacts.discards != wantDiscards {
					t.Fatal("stage settlement", files, artifacts.discards)
				}
			}
			if name == "confirmed readback" || name == "uncertain rollback" {
				if fault.writes != 2 || fault.readbacks != 1 {
					t.Fatal("checkpoint repeated", fault.writes, fault.readbacks)
				}
				if name == "uncertain rollback" && !errors.Is(err, xerror.ErrExecutionCommitUnknown) {
					t.Fatal(err)
				}
			}
			// Exercise D5's restart scan only after the synchronous attempt has returned.
			recovered, e := artifactstore.Open(db.StateDirectory())
			if e != nil {
				t.Fatal(e)
			}
			defer recovered.Close()
			ids, e := repo.ArtifactIDs(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			if _, e := recovered.Reconcile(t.Context(), ids); e != nil {
				t.Fatal(e)
			}
			after, e := filepath.Glob(filepath.Join(db.StateDirectory(), "artifacts", "*.png"))
			if e != nil || len(after) != count {
				t.Fatal("reconcile lost committed file or retained orphan", after, e)
			}
		})
	}
}
