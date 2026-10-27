package discovery

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/auth"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestPublisherLifecycle(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "runtime")
	publisher := testPublisher(t, directory)

	if err := os.MkdirAll(directory, constants.ConnectionDirectoryMode); err != nil {
		t.Fatalf("create stale directory: %v", err)
	}
	finalPath := filepath.Join(directory, constants.RuntimeConnectionFilename)
	if err := os.WriteFile(finalPath, []byte(`{"stale":true}`), constants.ConnectionFileMode); err != nil {
		t.Fatalf("write stale connection file: %v", err)
	}

	if err := publisher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	contents, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read published connection file: %v", err)
	}
	if bytes.Contains(contents, []byte("stale")) {
		t.Fatal("published connection file retained stale content")
	}
	if runtime.GOOS != "windows" {
		assertFileMode(t, directory, constants.ConnectionDirectoryMode)
		assertFileMode(t, finalPath, constants.ConnectionFileMode)
	}

	waitResult := make(chan error, 1)
	go func() {
		waitResult <- publisher.Wait()
	}()
	select {
	case err := <-waitResult:
		t.Fatalf("Wait() returned before shutdown: %v", err)
	default:
	}

	if err := publisher.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if _, err := os.Lstat(finalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("connection file still exists after shutdown: %v", err)
	}
	if err := <-waitResult; err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
}

func TestPublisherRejectsSymlinkDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link setup requires platform privileges on Windows")
	}
	t.Parallel()

	root := t.TempDir()
	directory := filepath.Join(root, "runtime")
	if err := os.Symlink(filepath.Join(root, "target"), directory); err != nil {
		t.Fatalf("create directory symlink: %v", err)
	}

	err := testPublisher(t, directory).Start(context.Background())
	if !errors.Is(err, xerror.ErrPrepareLocalAuthDirectory) {
		t.Fatalf("Start() error = %v, want %v", err, xerror.ErrPrepareLocalAuthDirectory)
	}
}

func TestPublisherDoesNotRemoveReplacedFile(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "runtime")
	publisher := testPublisher(t, directory)
	if err := publisher.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	finalPath := filepath.Join(directory, constants.RuntimeConnectionFilename)
	if err := os.Remove(finalPath); err != nil {
		t.Fatalf("remove owned connection file: %v", err)
	}
	if err := os.WriteFile(finalPath, []byte(`{"replacement":true}`), constants.ConnectionFileMode); err != nil {
		t.Fatalf("write replacement connection file: %v", err)
	}

	err := publisher.Shutdown(context.Background())
	if !errors.Is(err, xerror.ErrCleanupLocalAuthConnection) {
		t.Fatalf("Shutdown() error = %v, want %v", err, xerror.ErrCleanupLocalAuthConnection)
	}
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("replacement connection file was removed: %v", err)
	}
}

func TestPublisherErrorsDoNotExposePrivatePath(t *testing.T) {
	t.Parallel()

	privatePath := "private-user-location"
	publisher := testPublisher(t, privatePath)
	err := publisher.Start(context.Background())
	if err == nil {
		t.Fatal("Start() error = nil, want path resolution error")
	}
	if strings.Contains(err.Error(), privatePath) {
		t.Fatal("Start() error exposed the private path")
	}
}

func testPublisher(t *testing.T, directory string) *Publisher {
	t.Helper()

	session, err := auth.NewSession()
	if err != nil {
		t.Fatalf("newSession() error = %v", err)
	}

	return newPublisher(
		"127.0.0.1:8765",
		session,
		func() (string, error) { return directory, nil },
	)
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat protected path: %v", err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("protected path mode = %o, want %o", got, want)
	}
}
