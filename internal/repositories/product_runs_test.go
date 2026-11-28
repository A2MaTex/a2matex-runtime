package repositories

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func productAdmission() execution.Admission {
	a := testAdmission(1)
	a.Product = &execution.ProductProvenance{Package: packagevalidation.PublicPackage{ProductID: testExecutionID(30), ReleaseID: testExecutionID(31), Version: "0.1.0", PackageHash: strings.Repeat("a", 64)}, RuntimeIRHash: strings.Repeat("b", 64), RootHash: strings.Repeat("c", 64), RuntimeSessionID: testExecutionID(32), LeaseFamilyID: testExecutionID(33), Sequence: 1, RefreshAfter: time.Now().UTC().Truncate(time.Millisecond), ExpiresAt: time.Now().UTC().Truncate(time.Millisecond).Add(time.Minute)}
	return a
}
func TestProductProvenanceAtomicityKindAndCorruption(t *testing.T) {
	s := executionStore(t)
	a := productAdmission()
	// Force insertion failure inside the transaction; no group/device/event can escape it.
	if _, err := s.store.DB().Exec(`CREATE TRIGGER reject_product BEFORE INSERT ON product_run_provenance BEGIN SELECT RAISE(ABORT,'controlled'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(t.Context(), a); err == nil {
		t.Fatal("failed write admitted")
	}
	var count int
	if err := s.store.DB().QueryRow(`SELECT count(*) FROM run_groups`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if _, err := s.store.DB().Exec(`DROP TRIGGER reject_product`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	snap, err := s.SnapshotByID(t.Context(), a.RunGroupID)
	if err != nil || snap.Product == nil || *snap.Product != *a.Product {
		t.Fatal(snap, err)
	}
	if _, err := s.Admit(t.Context(), execution.Admission{RequestID: a.RequestID}); !errors.Is(err, xerror.ErrProductRunConflict) {
		t.Fatal("cross-kind replay", err)
	}
	// Durable classification must survive corrupt/missing provenance, not become Development.
	if _, err := s.store.DB().Exec(`DELETE FROM product_run_provenance`); err != nil {
		t.Fatal(err)
	}
	kind, err := s.ExecutionKind(t.Context(), a.RunGroupID)
	if err != nil || kind != constants.ExecutionKindProduct {
		t.Fatal(kind, err)
	}
	if _, err := s.SnapshotByID(t.Context(), a.RunGroupID); err == nil {
		t.Fatal("missing provenance served")
	}
}
func TestProductArtifactMetadataDeniedBeforeFileAccess(t *testing.T) {
	s := executionStore(t)
	a := productAdmission()
	if _, err := s.Admit(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	id := testExecutionID(50)
	artifact := testExecutionID(51)
	device := a.Devices[0].DeviceExecutionID
	if err := s.WithWriteTx(t.Context(), func(tx Tx) error {
		if _, err := tx.ExecContext(t.Context(), `INSERT INTO node_attempts(id,device_execution_id,sequence,node_id,attempt_number,status,started_at,finished_at,output_json) VALUES (?,?,1,'shot',1,'SUCCEEDED',1,2,'{}')`, id, device); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), `INSERT INTO artifacts(id,attempt_id,file_key,media_type,byte_length,width,height,digest,created_at) VALUES (?,?,'private-key','image/png',1,1,1,?,1)`, artifact, id, strings.Repeat("a", 64))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArtifactByID(context.Background(), artifact); !errors.Is(err, xerror.ErrExecutionNotFound) {
		t.Fatal("Product artifact leaked", err)
	}
}
