package artifactstore

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestStageRejectsInvalidPNGs(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var valid, oversizedAxis bytes.Buffer
	if err := png.Encode(&valid, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&oversizedAxis, image.NewRGBA(image.Rect(0, 0, constants.MaxScreenshotDimension+1, 1))); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{nil, []byte("not a png"), valid.Bytes()[:valid.Len()-2], append(bytes.Clone(valid.Bytes()), 1), oversizedAxis.Bytes(), make([]byte, constants.MaxScreenshotBytes+1)} {
		if _, err := store.Stage(t.Context(), "11111111-1111-4111-8111-111111111111", data); !errors.Is(err, xerror.ErrArtifactImageInvalid) {
			t.Fatalf("image of length %d: %v", len(data), err)
		}
	}
	if report, err := store.Reconcile(t.Context(), nil); err != nil || report != (ReconcileReport{}) {
		t.Fatalf("invalid images created entries: %+v, %v", report, err)
	}
}

func TestPublishCollisionKeepsOriginal(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const id = "11111111-1111-4111-8111-111111111111"
	finalKey := execution.ArtifactFileKey(id)
	tempKey := tempPrefix + id + tempSuffix
	final := filepath.Join(root, directoryName, finalKey)
	if err := os.WriteFile(final, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.stageFile(t.Context(), finalKey, tempKey, []byte("replacement")); !errors.Is(err, xerror.ErrExecutionPersistence) {
		t.Fatalf("destination collision: %v", err)
	}
	if contents, err := os.ReadFile(final); err != nil || string(contents) != "original" {
		t.Fatalf("original contents: %q, %v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(root, directoryName, tempKey)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file not cleaned up: %v", err)
	}
}

func TestReadRefusesSubstitutedSymlink(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var raw bytes.Buffer
	if err := png.Encode(&raw, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Stage(t.Context(), "11111111-1111-4111-8111-111111111111", raw.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, directoryName, artifact.FileKey)
	outside := filepath.Join(root, "outside.png")
	if err := os.Rename(path, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		if errors.Is(err, os.ErrPermission) || runtime.GOOS == "windows" {
			t.Skipf("creating a symlink requires additional host privileges: %v", err)
		}
		t.Fatal(err)
	}
	if file, err := store.Read(artifact); !errors.Is(err, xerror.ErrExecutionPersistence) {
		if file != nil {
			file.Close()
		}
		t.Fatalf("symlink read: %v", err)
	}
	if report, err := store.Reconcile(t.Context(), nil); err != nil || report.Skipped != 1 {
		t.Fatalf("symlink recovery: %+v, %v", report, err)
	}
	if data, err := os.ReadFile(outside); err != nil || !bytes.Equal(data, raw.Bytes()) {
		t.Fatal("artifact reconciliation touched the symlink target")
	}
}
