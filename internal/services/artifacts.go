package services

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/artifactstore"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/sqlitestore"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// ArtifactService coordinates the SQLite metadata and the local PNG store.
type ArtifactService struct {
	state      *sqlitestore.Store
	repository execution.ArtifactRepository
	logger     *slog.Logger
	done       chan struct{}
	mu         sync.RWMutex
	files      *artifactstore.Store
}

func NewArtifactService(state *sqlitestore.Store, repository execution.ArtifactRepository, logger *slog.Logger) *ArtifactService {
	return &ArtifactService{state: state, repository: repository, logger: logger, done: make(chan struct{})}
}

func (s *ArtifactService) Start(ctx context.Context) error {
	root := s.state.StateDirectory()
	if root == "" {
		return xerror.WithOperation("initialize_artifact_store", xerror.ErrExecutionPersistence)
	}
	committed, err := s.repository.ArtifactIDs(ctx)
	if err != nil {
		return err
	}
	files, err := artifactstore.Open(root)
	if err != nil {
		return err
	}
	report, err := files.Reconcile(ctx, committed)
	if err != nil {
		_ = files.Close()
		return err
	}
	if report.Skipped != 0 || report.RemovalFailures != 0 {
		s.logger.Warn("artifact recovery skipped entries", "component", "artifact_store",
			"event", "artifact_reconciliation_incomplete", "skipped", report.Skipped,
			"removal_failures", report.RemovalFailures)
	}
	s.mu.Lock()
	s.files = files
	s.mu.Unlock()
	return nil
}

func (s *ArtifactService) Wait() error {
	<-s.done
	return nil
}

func (s *ArtifactService) Shutdown(context.Context) error {
	defer close(s.done)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.files == nil {
		return nil
	}
	err := s.files.Close()
	s.files = nil
	return err
}

// Stage prepares a file for the active attempt identified by the workflow runner.
func (s *ArtifactService) Stage(ctx context.Context, attemptID string, png []byte) (execution.Artifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.files == nil {
		return execution.Artifact{}, xerror.WithOperation("stage_artifact_file", xerror.ErrExecutionPersistence)
	}
	return s.files.Stage(ctx, attemptID, png)
}

// Open returns the committed artifact and its verified handle. The caller closes the handle.
func (s *ArtifactService) Open(ctx context.Context, id string) (execution.Artifact, *os.File, error) {
	if !execution.ValidID(id) {
		return execution.Artifact{}, nil, xerror.ErrExecutionInvalidInput
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.files == nil {
		return execution.Artifact{}, nil, xerror.WithOperation("open_artifact_file", xerror.ErrExecutionPersistence)
	}
	artifact, err := s.repository.ArtifactByID(ctx, id)
	if err != nil {
		return execution.Artifact{}, nil, err
	}
	if artifact.ID != id {
		return execution.Artifact{}, nil, xerror.WithOperation("validate_artifact_metadata", xerror.ErrExecutionPersistence)
	}
	if err := ctx.Err(); err != nil {
		return execution.Artifact{}, nil, xerror.WithOperation("open_artifact_file", err)
	}
	file, err := s.files.Read(artifact)
	if err != nil {
		return execution.Artifact{}, nil, err
	}
	return artifact, file, nil
}

// DiscardUncommitted is called under the checkpoint writer gate after the transaction settles.
func (s *ArtifactService) DiscardUncommitted(ctx context.Context, id string) {
	if err := s.discardUncommitted(ctx, id); err != nil {
		s.logger.Error("artifact cleanup deferred to startup", "component", "artifact_store",
			"event", "artifact_discard_failed", "error", err.Error())
	}
}

func (s *ArtifactService) discardUncommitted(ctx context.Context, id string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.files == nil {
		return xerror.WithOperation("discard_artifact_file", xerror.ErrExecutionPersistence)
	}
	_, err := s.repository.ArtifactByID(ctx, id)
	if !errors.Is(err, xerror.ErrExecutionNotFound) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return xerror.WithOperation("discard_artifact_file", err)
	}
	return s.files.Discard(id)
}
