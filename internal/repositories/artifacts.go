package repositories

import (
	"context"
	"database/sql"
	"errors"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

var _ execution.ArtifactRepository = (*ExecutionRepository)(nil)

const artifactFields = `id, attempt_id, file_key, media_type, byte_length, width, height, digest, created_at`

func (s *ExecutionRepository) ArtifactByID(ctx context.Context, id string) (execution.Artifact, error) {
	db := s.store.DB()
	if db == nil {
		return execution.Artifact{}, missingStore(ctx, "open_artifact_metadata")
	}
	return readArtifact(ctx, db.QueryRowContext(ctx, `SELECT `+artifactFields+` FROM artifacts WHERE id = ?`, id))
}

func (s *ExecutionRepository) ArtifactIDs(ctx context.Context) (map[string]struct{}, error) {
	db := s.store.DB()
	if db == nil {
		return nil, missingStore(ctx, "open_artifact_metadata")
	}
	rows, err := db.QueryContext(ctx, `SELECT id FROM artifacts`)
	if err != nil {
		return nil, storageError(ctx, "read_artifact_inventory", err)
	}
	defer rows.Close()
	ids := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, storageError(ctx, "decode_artifact_inventory", err)
		}
		if !execution.ValidID(id) {
			return nil, storageError(ctx, "validate_artifact_inventory", nil)
		}
		ids[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, storageError(ctx, "read_artifact_inventory", err)
	}
	return ids, nil
}

func (tx Tx) InsertArtifact(ctx context.Context, artifact execution.Artifact) error {
	const query = `INSERT INTO artifacts (` + artifactFields + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	return tx.updateExpected(ctx, "insert_artifact", query, artifact.ID, artifact.AttemptID, artifact.FileKey,
		artifact.MediaType, artifact.ByteLength, artifact.Width, artifact.Height, artifact.Digest, artifact.CreatedAt)
}

func (tx Tx) artifactByAttempt(ctx context.Context, attemptID string) (*execution.Artifact, error) {
	artifact, err := readArtifact(ctx, tx.QueryRowContext(ctx,
		`SELECT `+artifactFields+` FROM artifacts WHERE attempt_id = ?`, attemptID))
	if errors.Is(err, xerror.ErrExecutionNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &artifact, nil
}

func readArtifact(ctx context.Context, row *sql.Row) (execution.Artifact, error) {
	var artifact execution.Artifact
	err := row.Scan(&artifact.ID, &artifact.AttemptID, &artifact.FileKey, &artifact.MediaType,
		&artifact.ByteLength, &artifact.Width, &artifact.Height, &artifact.Digest, &artifact.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Artifact{}, xerror.ErrExecutionNotFound
	}
	if err != nil {
		return execution.Artifact{}, storageError(ctx, "read_artifact_metadata", err)
	}
	return artifact, nil
}
