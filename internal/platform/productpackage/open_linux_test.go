package productpackage

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestReadLinks(t *testing.T) {
	raw := makeZIP(t, normalEntries(0))
	for _, tc := range []struct {
		name, target string
		outside      bool
	}{
		{"contained relative", "../inside.a2m", false},
		{"outside relative", "../../outside.a2m", true},
		{"outside absolute", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, path := installPackage(t, raw)
			root, err := ResolveLocation(request)
			if err != nil {
				t.Fatal(err)
			}
			target := tc.target
			if target == "" {
				target = filepath.Join(t.TempDir(), "outside.a2m")
			}
			resolved := target
			if !filepath.IsAbs(target) {
				resolved = filepath.Join(filepath.Dir(path), target)
			}
			if err := os.WriteFile(resolved, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			if tc.outside {
				assertReadError(t, request, xerror.ErrProductPackageUnavailable, "open_package_file")
				return
			}
			snapshot, err := Read(context.Background(), request)
			if err != nil || !bytes.Equal(snapshot.ZIPBytes, raw) {
				t.Fatalf("contained link: %v", err)
			}
			// The selected root itself may be a legitimate Documents symlink.
			alias := filepath.Join(t.TempDir(), "Documents")
			if err := os.Symlink(root.Directory, alias); err != nil {
				t.Fatal(err)
			}
			if _, err := Read(context.Background(), requestAt(alias)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReadLinkReplacementRace(t *testing.T) {
	raw := makeZIP(t, normalEntries(0))
	request, path := installPackage(t, raw)
	outsideEntries := normalEntries(0)
	outsideEntries[0].data = []byte("outside SECRET")
	outside := filepath.Join(t.TempDir(), "outside.a2m")
	if err := os.WriteFile(outside, makeZIP(t, outsideEntries), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 100; i++ {
			next := path + ".next"
			if err := os.Symlink(outside, next); err != nil {
				done <- err
				return
			}
			if err := os.Rename(next, path); err != nil {
				done <- err
				return
			}
			if err := os.WriteFile(next, raw, 0600); err != nil {
				done <- err
				return
			}
			if err := os.Rename(next, path); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	var failure error
	for i := 0; i < 100; i++ {
		snapshot, err := Read(context.Background(), request)
		if err == nil && !bytes.Equal(snapshot.ZIPBytes, raw) {
			failure = errors.New("read outside-root bytes")
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if failure != nil {
		t.Fatal(failure)
	}
}

func TestReadFIFO(t *testing.T) {
	const childRoot = "A2MATEX_J2_FIFO_TEST_ROOT"
	if root := os.Getenv(childRoot); root != "" {
		assertReadError(t, requestAt(root), xerror.ErrProductPackageInvalid, "read_package_snapshot")
		return
	}
	request, path := installPackage(t, []byte("replaced"))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	location, err := ResolveLocation(request)
	if err != nil {
		t.Fatal(err)
	}
	// A subprocess deadline also catches regressions that block during open.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReadFIFO$")
	command.Env = append(os.Environ(), childRoot+"="+location.Directory)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("FIFO must be refused promptly: %v %s", err, output)
	}
}

func TestReadPermissionAndParentEscape(t *testing.T) {
	request, path := installPackage(t, makeZIP(t, normalEntries(0)))
	if os.Geteuid() != 0 {
		if err := os.Chmod(path, 0000); err != nil {
			t.Fatal(err)
		}
		assertReadError(t, request, xerror.ErrProductPackageUnavailable, "open_package_file")
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	location, err := ResolveLocation(request)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(location.Directory, testProductID)
	if err := os.RemoveAll(parent); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, parent); err != nil {
		t.Fatal(err)
	}
	assertReadError(t, request, xerror.ErrProductPackageUnavailable, "open_package_file")
}
