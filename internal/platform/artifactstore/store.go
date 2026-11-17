// Package artifactstore manages files within the Runtime's screenshot directory.
package artifactstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image/png"
	"io"
	"os"
	"strings"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

const (
	directoryName = "artifacts"
	tempPrefix    = ".stage-"
	tempSuffix    = ".tmp"
	scanBatchSize = 256
)

var errUnsafeEntry = errors.New("unsafe artifact file")

type Store struct {
	dir *os.File
}

func Open(stateDirectory string) (*Store, error) {
	dir, err := openArtifactDirectory(stateDirectory)
	if err != nil {
		return nil, failure("open_artifact_directory")
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Close() error {
	if err := s.dir.Close(); err != nil {
		return failure("close_artifact_directory")
	}
	return nil
}

func (s *Store) Stage(ctx context.Context, attemptID string, data []byte) (execution.Artifact, error) {
	if !execution.ValidID(attemptID) {
		return execution.Artifact{}, xerror.ErrExecutionInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return execution.Artifact{}, xerror.WithOperation("validate_artifact_image", err)
	}
	if len(data) == 0 || len(data) > constants.MaxScreenshotBytes {
		return execution.Artifact{}, xerror.WithOperation("validate_artifact_image", xerror.ErrArtifactImageInvalid)
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || !execution.ValidScreenshotDimensions(config.Width, config.Height) {
		return execution.Artifact{}, xerror.WithOperation("validate_artifact_image", xerror.ErrArtifactImageInvalid)
	}
	imageBytes := bytes.NewReader(data)
	if _, err := png.Decode(imageBytes); err != nil || imageBytes.Len() != 0 {
		return execution.Artifact{}, xerror.WithOperation("decode_artifact_image", xerror.ErrArtifactImageInvalid)
	}
	if err := ctx.Err(); err != nil {
		return execution.Artifact{}, xerror.WithOperation("stage_artifact_file", err)
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return execution.Artifact{}, failure("allocate_artifact_id")
	}
	digest := sha256.Sum256(data)
	artifact := execution.Artifact{
		ID: id.String(), AttemptID: attemptID, MediaType: constants.ScreenshotMediaType,
		ByteLength: int64(len(data)), Width: config.Width, Height: config.Height,
		Digest: hex.EncodeToString(digest[:]), CreatedAt: time.Now().UnixMilli(),
	}
	artifact.FileKey = execution.ArtifactFileKey(artifact.ID)
	if err := s.stageFile(ctx, artifact.FileKey, tempPrefix+artifact.ID+tempSuffix, data); err != nil {
		return execution.Artifact{}, err
	}
	return artifact, nil
}

func (s *Store) stageFile(ctx context.Context, final, temp string, data []byte) error {
	file, err := createEntry(s.dir, temp)
	if err != nil {
		return failure("create_artifact_temp")
	}
	cleanup := temp
	defer func() {
		if cleanup != "" {
			_ = removeEntry(s.dir, cleanup)
		}
	}()
	_, writeErr := file.Write(data)
	var syncErr error
	if writeErr == nil {
		syncErr = file.Sync()
	}
	closeErr := file.Close()
	switch {
	case writeErr != nil:
		return failure("write_artifact_temp")
	case syncErr != nil:
		return failure("sync_artifact_temp")
	case closeErr != nil:
		return failure("close_artifact_temp")
	}
	if err := ctx.Err(); err != nil {
		return xerror.WithOperation("publish_artifact_file", err)
	}
	if err := publishEntry(s.dir, temp, final); err != nil {
		return failure("publish_artifact_file")
	}
	cleanup = final
	if err := syncDirectory(s.dir); err != nil {
		return failure("sync_artifact_directory")
	}
	if err := ctx.Err(); err != nil {
		return xerror.WithOperation("publish_artifact_file", err)
	}
	cleanup = ""
	return nil
}

func (s *Store) Read(artifact execution.Artifact) (*os.File, error) {
	if !artifact.Valid() {
		return nil, failure("validate_artifact_metadata")
	}
	file, err := openEntry(s.dir, artifact.FileKey)
	if errors.Is(err, os.ErrNotExist) {
		return nil, xerror.WithOperation("open_artifact_file", xerror.ErrExecutionNotFound)
	}
	if err != nil {
		return nil, failure("open_artifact_file")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != artifact.ByteLength {
		_ = file.Close()
		return nil, failure("verify_artifact_file")
	}
	return file, nil
}

func (s *Store) Discard(id string) error {
	if !execution.ValidID(id) {
		return xerror.ErrExecutionInvalidInput
	}
	if err := removeEntry(s.dir, execution.ArtifactFileKey(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return failure("discard_artifact_file")
	}
	return nil
}

type ReconcileReport struct {
	Skipped, RemovalFailures int
}

// Reconcile runs once after opening the directory, before accepting writes.
func (s *Store) Reconcile(ctx context.Context, committed map[string]struct{}) (ReconcileReport, error) {
	var report ReconcileReport
	for {
		if err := ctx.Err(); err != nil {
			return report, xerror.WithOperation("scan_artifact_directory", err)
		}
		entries, err := s.dir.Readdirnames(scanBatchSize)
		if err != nil && !errors.Is(err, io.EOF) {
			return report, failure("scan_artifact_directory")
		}
		for _, name := range entries {
			if err := ctx.Err(); err != nil {
				return report, xerror.WithOperation("scan_artifact_directory", err)
			}
			id, final := strings.CutSuffix(name, ".png")
			if !final {
				temp, hasPrefix := strings.CutPrefix(name, tempPrefix)
				var hasSuffix bool
				id, hasSuffix = strings.CutSuffix(temp, tempSuffix)
				if !hasPrefix || !hasSuffix {
					report.Skipped++
					continue
				}
			}
			if !execution.ValidID(id) {
				report.Skipped++
				continue
			}
			if _, referenced := committed[id]; final && referenced {
				continue
			}
			if removeErr := removeEntry(s.dir, name); errors.Is(removeErr, errUnsafeEntry) {
				report.Skipped++
			} else if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				report.RemovalFailures++
			}
		}
		if errors.Is(err, io.EOF) {
			return report, nil
		}
	}
}

func failure(operation string) error {
	return xerror.WithOperation(operation, xerror.ErrExecutionPersistence)
}
