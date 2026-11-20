package attempts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

type screenshotFiles struct {
	stage            func(context.Context, string, []byte) (execution.Artifact, error)
	discard          func(context.Context, string)
	stages, discards int
}

func (s *screenshotFiles) Stage(ctx context.Context, id string, data []byte) (execution.Artifact, error) {
	s.stages++
	if s.stage != nil {
		return s.stage(ctx, id, data)
	}
	return syntheticArtifact(id), nil
}
func (s *screenshotFiles) DiscardUncommitted(ctx context.Context, id string) {
	s.discards++
	if s.discard != nil {
		s.discard(ctx, id)
	}
}
func syntheticArtifact(attempt string) execution.Artifact {
	id := uuid.NewString()
	return execution.Artifact{ID: id, AttemptID: attempt, FileKey: execution.ArtifactFileKey(id), MediaType: constants.ScreenshotMediaType,
		ByteLength: 3, Width: 2, Height: 1, Digest: strings.Repeat("0", 64), CreatedAt: time.Now().UnixMilli()}
}
func screenshotRequest() Request {
	r := requestFor(constants.ScreenshotNodeType)
	r.Node.Config = ir.Object{"targetMode": []byte(`"FULL_SCREEN"`)}
	return r
}

func TestScreenshotGuardAndBeginPrecedeCapture(t *testing.T) {
	for _, name := range []string{"missing store", "stale begin", "failed begin"} {
		t.Run(name, func(t *testing.T) {
			r, cp, files, calls := screenshotRequest(), &checkpointStub{}, &screenshotFiles{}, 0
			deps := dependencies(t, r, func(context.Context, registry.Invocation) (map[string]any, error) {
				calls++
				return map[string]any{}, nil
			}, cp)
			if name != "missing store" {
				deps.Artifacts = files
			}
			if name == "stale begin" {
				cp.begin = func(context.Context, execution.BeginAttemptRequest) (execution.CheckpointResult, error) {
					return execution.CheckpointResult{Disposition: constants.CheckpointStale}, nil
				}
			}
			if name == "failed begin" {
				cp.begin = func(context.Context, execution.BeginAttemptRequest) (execution.CheckpointResult, error) {
					return execution.CheckpointResult{}, xerror.ErrExecutionPersistence
				}
			}
			got, err := coordinator(t, deps).Run(t.Context(), r)
			if err == nil || calls != 0 || files.stages != 0 || cp.finishes != 0 || (name == "missing store" && cp.begins != 0) {
				t.Fatal(got, err, calls, cp)
			}
		})
	}
}

func TestScreenshotPairingAndCallbackContract(t *testing.T) {
	for _, name := range []string{"success", "wrong output", "missing stage", "invalid output", "repeated callback", "ignored stage failure", "foreign metadata", "invalid metadata"} {
		t.Run(name, func(t *testing.T) {
			r, cp, files := screenshotRequest(), &checkpointStub{}, &screenshotFiles{}
			r.Node.ErrorPolicy = constants.ErrorPolicyErrorBranch
			var owned string
			if name == "ignored stage failure" {
				files.stage = func(context.Context, string, []byte) (execution.Artifact, error) {
					return execution.Artifact{}, xerror.ErrExecutionPersistence
				}
			}
			if name == "foreign metadata" || name == "invalid metadata" {
				files.stage = func(_ context.Context, id string, _ []byte) (execution.Artifact, error) {
					a := syntheticArtifact(id)
					if name == "foreign metadata" {
						a.AttemptID = uuid.NewString()
					} else {
						a.Width = 0
					}
					return a, nil
				}
			}
			var retained func(context.Context, []byte) (string, error)
			handler := func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
				if call.ADBSerial != r.ADBSerial {
					t.Error("serial changed")
				}
				retained = call.StageScreenshot
				if name == "missing stage" {
					return map[string]any{"artifactId": uuid.NewString()}, nil
				}
				id, err := call.StageScreenshot(ctx, []byte("synthetic"))
				owned = id
				if name == "ignored stage failure" {
					return map[string]any{"artifactId": uuid.NewString()}, nil
				}
				if err != nil {
					return nil, err
				}
				if name == "repeated callback" {
					_, _ = call.StageScreenshot(ctx, []byte("again"))
				}
				if name == "wrong output" {
					id = uuid.NewString()
				}
				if name == "invalid output" {
					return map[string]any{"artifactId": 123}, nil
				}
				return map[string]any{"artifactId": id}, nil
			}
			deps := dependencies(t, r, handler, cp)
			deps.Artifacts = files
			got, err := coordinator(t, deps).Run(t.Context(), r)
			if name == "ignored stage failure" {
				if !errors.Is(err, xerror.ErrExecutionPersistence) || cp.finishes != 0 {
					t.Fatal(got, err, cp)
				}
			} else {
				if err != nil || !got.Finished || cp.finishes != 1 {
					t.Fatal(got, err, cp)
				}
				if name == "success" {
					if got.Outcome.Status != constants.ExecutionSucceeded || cp.last.Artifact == nil || cp.last.Artifact.ID != owned || cp.last.DiscardArtifactID != "" {
						t.Fatal(got, cp.last)
					}
				} else {
					code := constants.InternalError
					if name == "wrong output" || name == "invalid output" {
						code = constants.NodeOutputInvalid
					}
					if got.Outcome.Status != constants.ExecutionFailed || got.Outcome.Error == nil || got.Outcome.Error.Code != code || cp.last.Artifact != nil {
						t.Fatal(got, cp.last)
					}
					if owned != "" && cp.last.DiscardArtifactID != owned {
						t.Fatal("owned stage lost", cp.last)
					}
					if owned == "" && cp.last.DiscardArtifactID != "" {
						t.Fatal("foreign stage selected for discard")
					}
				}
			}
			if files.discards != 0 {
				t.Fatal("G19 discarded a submitted or unknown stage")
			}
			before := files.stages
			if _, err := retained(t.Context(), nil); err == nil || files.stages != before {
				t.Fatal("callback remained active")
			}
		})
	}
}

func TestScreenshotStopAfterStageAndSettlementOwnership(t *testing.T) {
	for _, name := range []string{"owner loss", "intentional", "control only", "deadline", "confirmed then stop", "finish rejected", "stale finish", "duplicate finish", "fatal before finish"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r, cp, files := screenshotRequest(), &checkpointStub{}, &screenshotFiles{}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				accepted := false
				r.Control = func() Control {
					return Control{GroupCancellation: accepted, ShutdownDeadline: time.Now().Add(time.Second)}
				}
				var owned string
				files.stage = func(ctx context.Context, id string, _ []byte) (execution.Artifact, error) {
					a := syntheticArtifact(id)
					owned = a.ID
					switch name {
					case "owner loss":
						cancel()
					case "intentional":
						accepted = true
						cancel()
					case "control only":
						accepted = true
					case "deadline":
						time.Sleep(2 * time.Second)
					}
					return a, nil
				}
				files.discard = func(ctx context.Context, id string) {
					if ctx.Err() != nil || id != owned {
						t.Error("cleanup context/identity", ctx.Err(), id)
					}
					if _, ok := ctx.Deadline(); !ok {
						t.Error("unbounded cleanup")
					}
				}
				if name == "confirmed then stop" {
					cp.finish = func(context.Context, execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
						accepted = true
						cancel()
						return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "5"}, nil
					}
				}
				if name == "finish rejected" {
					cp.finish = func(context.Context, execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
						return execution.CheckpointResult{}, xerror.ErrCheckpointStopped
					}
				}
				if name == "stale finish" || name == "duplicate finish" {
					cp.finish = func(context.Context, execution.FinishAttemptRequest) (execution.CheckpointResult, error) {
						disposition := constants.CheckpointStale
						if name == "duplicate finish" {
							disposition = constants.CheckpointAlreadyFinalized
						}
						return execution.CheckpointResult{Disposition: disposition, Sequence: 1, Cursor: "5"}, nil
					}
				}
				handler := func(ctx context.Context, call registry.Invocation) (map[string]any, error) {
					id, err := call.StageScreenshot(ctx, []byte("synthetic"))
					if err != nil {
						return nil, err
					}
					if name == "fatal before finish" {
						return nil, xerror.ErrExecutionPersistence
					}
					return map[string]any{"artifactId": id}, nil
				}
				deps := dependencies(t, r, handler, cp)
				deps.Artifacts = files
				got, err := coordinator(t, deps).Run(ctx, r)
				unsubmitted := name == "owner loss" || name == "fatal before finish"
				if unsubmitted {
					if err == nil || cp.finishes != 0 || files.discards != 1 || got.Finished {
						t.Fatal(got, err, cp, files.discards)
					}
					return
				}
				if cp.finishes != 1 || files.discards != 0 {
					t.Fatal("cleanup ownership", cp.finishes, files.discards)
				}
				if name == "finish rejected" {
					if !errors.Is(err, xerror.ErrCheckpointStopped) || got.Finished {
						t.Fatal(got, err)
					}
					return
				}
				if name == "stale finish" || name == "duplicate finish" {
					if err == nil || got.Finished || got.NextNodeID != "" {
						t.Fatal(got, err)
					}
					return
				}
				want := constants.ExecutionCancelled
				if name == "deadline" {
					want = constants.ExecutionTimedOut
				}
				if name == "confirmed then stop" {
					want = constants.ExecutionSucceeded
				}
				if err != nil || !got.Finished || got.Outcome.Status != want || cp.last.Status != want {
					t.Fatal(got, err, cp.last)
				}
				if want == constants.ExecutionSucceeded {
					if cp.last.Artifact == nil {
						t.Fatal("committed artifact lost")
					}
				} else if cp.last.Artifact != nil || cp.last.DiscardArtifactID != owned {
					t.Fatal("cancelled artifact retained", cp.last)
				}
			})
		})
	}
}

func TestScreenshotRetryNeedsExistingSafeEvidence(t *testing.T) {
	r, cp, files := screenshotRequest(), &checkpointStub{}, &screenshotFiles{}
	r.Node.RetryPolicy.MaxAttempts = 2
	deps := dependencies(t, r, func(context.Context, registry.Invocation) (map[string]any, error) {
		return nil, xerror.CommandError{Code: constants.CommandStartFailed, Message: "controlled pre-dispatch failure", Retryable: true}
	}, cp)
	deps.Artifacts = files
	got, err := coordinator(t, deps).Run(t.Context(), r)
	if err != nil || !got.Finished || got.Retry == nil || got.NextNodeID != r.Node.ID || cp.finishes != 1 || cp.last.Artifact != nil || cp.last.DiscardArtifactID != "" || files.stages != 0 {
		t.Fatal(got, err, cp.last)
	}
}

func TestScreenshotCallbackUsesBoundIdentityAndContext(t *testing.T) {
	r := screenshotRequest()
	files := &screenshotFiles{}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	files.stage = func(got context.Context, id string, data []byte) (execution.Artifact, error) {
		if got != ctx || id != r.Ref.AttemptID || string(data) != "png" {
			t.Fatal("staging changed context/identity/data")
		}
		return syntheticArtifact(id), nil
	}
	s := screenshotStage{store: files, attemptID: r.Ref.AttemptID}
	id, err := s.stage(t.Context(), ctx, []byte("png"))
	if err != nil || s.artifact == nil || id != s.artifact.ID {
		t.Fatal(id, err)
	}
	s.cleanup(t.Context(), r)
	if files.discards != 1 {
		t.Fatal(files.discards)
	}
}
