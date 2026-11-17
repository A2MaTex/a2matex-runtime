package repositories

import (
	"bytes"
	"context"
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
	"github.com/A2MaTex/a2matex-runtime/internal/services"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func startArtifactService(t *testing.T, repo *ExecutionRepository) *services.ArtifactService {
	t.Helper()
	service := services.NewArtifactService(repo.store, repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := service.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return service
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	pixels := image.NewRGBA(image.Rect(0, 0, 4, 3))
	pixels.Set(1, 1, color.RGBA{R: 255, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, pixels); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func stagedFinish(t *testing.T, store *services.ArtifactService, begin execution.BeginAttemptRequest, png []byte) execution.FinishAttemptRequest {
	t.Helper()
	artifact, err := store.Stage(t.Context(), begin.AttemptID, png)
	if err != nil {
		t.Fatal(err)
	}
	finish := finishFixture(begin)
	finish.Now = time.Now()
	finish.NodeType = constants.ScreenshotNodeType
	finish.Artifact = &artifact
	output, err := json.Marshal(map[string]string{"artifactId": artifact.ID})
	if err != nil {
		t.Fatal(err)
	}
	finish.Output = output
	return finish
}

func TestScreenshotCommitAndDuplicateCleanup(t *testing.T) {
	repo, checkpoint, admission := checkpointFixture(t, 1)
	store := startArtifactService(t, repo)
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	begin := beginFixture(admission, 0, 100)
	begin.Now = time.Now().Add(-time.Second)
	requireBegin(t, checkpoint, begin, 1, "4")
	service := services.NewCheckpointService(repo, store)
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })

	raw := testPNG(t)
	finish := stagedFinish(t, store, begin, raw)
	original := *finish.Artifact
	if _, _, err := store.Open(t.Context(), original.ID); !errors.Is(err, xerror.ErrExecutionNotFound) {
		t.Fatalf("staged file is already visible: %v", err)
	}
	if result, err := service.FinishAttempt(t.Context(), execution.FinishAttemptRequest{
		AttemptRef: begin.AttemptRef, NodeType: constants.ScreenshotNodeType,
		Status: constants.ExecutionSucceeded, Output: finish.Output, Now: finish.Now,
	}); !errors.Is(err, xerror.ErrExecutionInvalidInput) || result.Disposition != "" {
		t.Fatalf("missing artifact record accepted: %+v, %v", result, err)
	}
	requireFinish(t, service, finish, 1, "7")
	duplicate := stagedFinish(t, store, begin, raw)
	for _, request := range []execution.FinishAttemptRequest{finish, duplicate} {
		result, err := service.FinishAttempt(t.Context(), request)
		if err != nil || result.Disposition != constants.CheckpointAlreadyFinalized {
			t.Fatalf("duplicate: %+v, %v", result, err)
		}
	}
	dir := filepath.Join(repo.store.StateDirectory(), "artifacts")
	if _, err := os.Stat(filepath.Join(dir, duplicate.Artifact.FileKey)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("duplicate stage still exists: %v", err)
	}
	metadata, file, err := store.Open(t.Context(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || metadata != original || metadata.Width != 4 || metadata.Height != 3 || !bytes.Equal(contents, raw) {
		t.Fatalf("committed PNG: %+v, %v", metadata, err)
	}
	if err := os.Truncate(filepath.Join(dir, original.FileKey), 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Open(t.Context(), original.ID); !errors.Is(err, xerror.ErrExecutionPersistence) {
		t.Fatalf("length mismatch: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, original.FileKey)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Open(t.Context(), original.ID); !errors.Is(err, xerror.ErrExecutionNotFound) {
		t.Fatalf("missing committed file: %v", err)
	}
}

func TestScreenshotCheckpointRollbackDiscardsStage(t *testing.T) {
	repo, checkpoint, admission := checkpointFixture(t, 1)
	store := startArtifactService(t, repo)
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	begin := beginFixture(admission, 0, 110)
	begin.Now = time.Now().Add(-time.Second)
	requireBegin(t, checkpoint, begin, 1, "4")
	finish := stagedFinish(t, store, begin, testPNG(t))
	service := services.NewCheckpointService(repo, store)
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	if _, err := repo.store.DB().ExecContext(t.Context(), `CREATE TRIGGER fail_artifact_finish BEFORE INSERT ON execution_events
	BEGIN SELECT RAISE(ABORT, 'simulated failure'); END`); err != nil {
		t.Fatal(err)
	}
	if result, err := service.FinishAttempt(t.Context(), finish); !errors.Is(err, xerror.ErrCheckpointFatal) || result.Disposition != "" {
		t.Fatalf("failed finish: %+v, %v", result, err)
	}
	assertUnfinishedCheckpoint(t, repo, begin)
	if _, err := repo.ArtifactByID(t.Context(), finish.Artifact.ID); !errors.Is(err, xerror.ErrExecutionNotFound) {
		t.Fatalf("metadata survived rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.store.StateDirectory(), "artifacts", finish.Artifact.FileKey)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file survived rollback: %v", err)
	}
}

func TestScreenshotUncertainCommitUsesArtifactEvidence(t *testing.T) {
	for _, test := range []struct {
		name                    string
		commit, missingArtifact bool
	}{
		{name: "matching commit", commit: true},
		{name: "unconfirmed rollback"},
		{name: "matching attempt without artifact evidence", commit: true, missingArtifact: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, checkpoint, admission := checkpointFixture(t, 1)
			store := startArtifactService(t, repo)
			begin := beginFixture(admission, 0, 120)
			begin.Now = time.Now().Add(-time.Second)
			requireBegin(t, checkpoint, begin, 1, "4")
			finish := stagedFinish(t, store, begin, testPNG(t))
			uncertain := &unconfirmedCheckpointRepo{ExecutionRepository: repo, commit: test.commit}
			var evidence execution.CheckpointRepository = uncertain
			if test.missingArtifact {
				evidence = missingArtifactReadback{uncertain}
			}
			service := services.NewCheckpointService(evidence, store)
			result, err := service.FinishAttempt(t.Context(), finish)
			if test.commit && !test.missingArtifact {
				if err != nil || result.Disposition != constants.CheckpointCommitted {
					t.Fatalf("valid read-back: %+v, %v", result, err)
				}
			} else if !errors.Is(err, xerror.ErrCheckpointFatal) {
				t.Fatalf("unresolved read-back: %+v, %v", result, err)
			}
			path := filepath.Join(repo.store.StateDirectory(), "artifacts", finish.Artifact.FileKey)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("file deleted before startup reconciliation: %v", err)
			}
			if err := service.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := store.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			restarted := startArtifactService(t, repo)
			defer restarted.Shutdown(context.Background())
			_, err = os.Stat(path)
			if test.commit && err != nil || !test.commit && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("file after startup reconciliation: %v", err)
			}
		})
	}
}

type missingArtifactReadback struct{ *unconfirmedCheckpointRepo }

func (s missingArtifactReadback) ReadCheckpoint(ctx context.Context, ref execution.AttemptRef) (execution.CheckpointState, int64, error) {
	state, watermark, err := s.unconfirmedCheckpointRepo.ReadCheckpoint(ctx, ref)
	state.Artifact = nil
	return state, watermark, err
}

func TestArtifactMetadataReadFailurePreservesFiles(t *testing.T) {
	repo := executionStore(t)
	store := startArtifactService(t, repo)
	stage, err := store.Stage(t.Context(), testExecutionID(130), testPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.store.DB().ExecContext(t.Context(), "DROP TABLE artifacts"); err != nil {
		t.Fatal(err)
	}
	restarted := services.NewArtifactService(repo.store, repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := restarted.Start(t.Context()); !errors.Is(err, xerror.ErrExecutionPersistence) {
		t.Fatalf("metadata failure did not fail startup: %v", err)
	}
	path := filepath.Join(repo.store.StateDirectory(), "artifacts", stage.FileKey)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("metadata lookup failure removed a file: %v", err)
	}
}

func TestScreenshotUnpublishedFinishDiscardsStage(t *testing.T) {
	for _, outcome := range []string{"stale", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			repo, checkpoint, admission := checkpointFixture(t, 1)
			store := startArtifactService(t, repo)
			defer store.Shutdown(context.Background())
			begin := beginFixture(admission, 0, 150)
			begin.Now = time.Now().Add(-time.Second)
			requireBegin(t, checkpoint, begin, 1, "4")
			finish := stagedFinish(t, store, begin, testPNG(t))
			stage := *finish.Artifact
			want := constants.CheckpointStale
			if outcome == "stale" {
				finish.NodeID = "superseded_node"
			} else {
				finish.Status, finish.GroupCancellation = constants.ExecutionCancelled, true
				finish.DiscardArtifactID, finish.Artifact, finish.Output = stage.ID, nil, nil
				want = constants.CheckpointCommitted
			}
			service := services.NewCheckpointService(repo, store)
			defer service.Shutdown(context.Background())
			if result, err := service.FinishAttempt(t.Context(), finish); err != nil || result.Disposition != want {
				t.Fatalf("finish: %+v, %v", result, err)
			}
			if _, err := repo.ArtifactByID(t.Context(), stage.ID); !errors.Is(err, xerror.ErrExecutionNotFound) {
				t.Fatalf("unexpected metadata: %v", err)
			}
			if _, err := os.Stat(filepath.Join(repo.store.StateDirectory(), "artifacts", stage.FileKey)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unused screenshot was not discarded: %v", err)
			}
		})
	}
}
