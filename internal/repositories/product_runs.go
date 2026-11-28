package repositories

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func (s *ExecutionRepository) ExecutionKind(ctx context.Context, id string) (string, error) {
	var kind string
	err := s.withReadTx(ctx, func(tx Tx) error { var err error; kind, err = executionKind(ctx, tx, id); return err })
	return kind, err
}
func executionKind(ctx context.Context, tx Tx, id string) (string, error) {
	var kind string
	err := tx.QueryRowContext(ctx, "SELECT execution_kind FROM run_groups WHERE id = ?", id).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", xerror.ErrExecutionNotFound
	}
	if err != nil {
		return "", storageError(ctx, "read_execution_kind", err)
	}
	if kind != constants.ExecutionKindDevelopment && kind != constants.ExecutionKindProduct {
		return "", xerror.ErrRecoveryIntegrity
	}
	return kind, nil
}
func readProduct(ctx context.Context, tx Tx, id string) (*execution.ProductProvenance, error) {
	var p execution.ProductProvenance
	var refresh, expires int64
	err := tx.QueryRowContext(ctx, `SELECT product_id,release_id,release_version,package_hash,runtime_ir_hash,package_root_hash,runtime_session_id,lease_family_id,lease_sequence,refresh_after,expires_at FROM product_run_provenance WHERE run_group_id=?`, id).Scan(&p.Package.ProductID, &p.Package.ReleaseID, &p.Package.Version, &p.Package.PackageHash, &p.RuntimeIRHash, &p.RootHash, &p.RuntimeSessionID, &p.LeaseFamilyID, &p.Sequence, &refresh, &expires)
	if err != nil {
		return nil, storageError(ctx, "read_product_provenance", err)
	}
	p.RefreshAfter, p.ExpiresAt = time.UnixMilli(refresh).UTC(), time.UnixMilli(expires).UTC()
	if !execution.ValidProductProvenance(&p) {
		return nil, xerror.ErrRecoveryIntegrity
	}
	return &p, nil
}
