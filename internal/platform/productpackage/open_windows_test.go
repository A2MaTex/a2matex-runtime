package productpackage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestWindowsRootSyntax(t *testing.T) {
	for _, root := range []string{`C:relative`, `\relative`, `\\server`, `\\server\`,
		`\\?\C:\packages`, `\\.\C:\packages`, `\??\C:\packages`, `\\??\C:\packages`} {
		assertReadError(t, requestAt(root), xerror.ErrProductPackageLocation, "decode_package_location")
	}
	for _, root := range []string{`c:\Users\Admin\OneDrive\Documents\a2matex\products\..\products\`, `c:/Users/Admin/OneDrive/Documents/a2matex/products`, `\\server\share\packages`} {
		location, err := ResolveLocation(requestAt(root))
		if err != nil {
			t.Fatalf("valid native root rejected: %v", err)
		}
		if strings.HasPrefix(root, "c:") && !strings.HasPrefix(location.Directory, "C:") {
			t.Fatal("drive not normalized")
		}
	}
}

func TestWindowsReparseContainment(t *testing.T) {
	request, path := installPackage(t, makeZIP(t, normalEntries(0)))
	outside := filepath.Join(t.TempDir(), "outside.a2m")
	if err := os.WriteFile(outside, []byte("outside SECRET"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("native symlink privileges unavailable: %v", err)
	}
	assertReadError(t, request, xerror.ErrProductPackageUnavailable, "open_package_file")
}

func TestWindowsUNCRead(t *testing.T) {
	share := os.Getenv("A2MATEX_J2_TEST_UNC_ROOT")
	if share == "" {
		t.Skip("set A2MATEX_J2_TEST_UNC_ROOT to an accessible UNC share for native acceptance")
	}
	if !strings.HasPrefix(share, `\\`) {
		t.Fatal("test root must be UNC")
	}
	root, err := os.MkdirTemp(share, "a2matex-j2-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	path := filepath.Join(root, filepath.FromSlash(testReference))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, makeZIP(t, normalEntries(0)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(context.Background(), requestAt(root)); err != nil {
		t.Fatal(err)
	}
}
