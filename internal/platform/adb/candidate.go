package adb

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const platformToolsDir = "platform-tools"

func executableName() string {
	if runtime.GOOS == "windows" {
		return "adb.exe"
	}
	return "adb"
}

func (r *Resolver) selectCandidate() (Toolchain, error) {
	if err := r.cfg.Validate(); err != nil {
		return Toolchain{}, invalidCandidate(Configured)
	}
	if r.cfg.ExecutablePath != "" {
		return fileCandidate(r.cfg.ExecutablePath, "", Configured)
	}

	installed, err := r.executable()
	if err != nil || !filepath.IsAbs(installed) {
		return Toolchain{}, xerror.ErrADBInstallLocation
	}
	installed, err = filepath.EvalSymlinks(installed)
	if err != nil {
		return Toolchain{}, xerror.ErrADBInstallLocation
	}
	bundleDir := filepath.Join(filepath.Dir(installed), platformToolsDir)
	bundlePath := filepath.Join(bundleDir, executableName())
	candidate, found, err := optionalCandidate(bundlePath, bundleDir, Bundled)
	if found || err != nil {
		return candidate, err
	}

	if r.cfg.AllowPathFallback {
		for _, dir := range filepath.SplitList(r.path()) {
			if !filepath.IsAbs(dir) {
				continue
			}
			candidate, found, err = optionalCandidate(filepath.Join(dir, executableName()), "", DevelopmentPath)
			if found || err != nil {
				return candidate, err
			}
		}
	}
	return Toolchain{}, xerror.ErrADBNotFound
}

func optionalCandidate(path, within string, source Source) (Toolchain, bool, error) {
	// An existing broken link is a failed candidate, even if its target is absent.
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Toolchain{}, false, nil
		}
		return Toolchain{}, false, invalidCandidate(source)
	}
	candidate, err := fileCandidate(path, within, source)
	return candidate, true, err
}

func fileCandidate(path, within string, source Source) (Toolchain, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Toolchain{}, invalidCandidate(source)
	}
	if within != "" {
		relative, err := filepath.Rel(within, resolved)
		if err != nil || !filepath.IsLocal(relative) {
			return Toolchain{}, invalidCandidate(source)
		}
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return Toolchain{}, invalidCandidate(source)
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(resolved), ".exe") {
		return Toolchain{}, invalidCandidate(source)
	}
	return Toolchain{Executable: resolved, Source: source}, nil
}

func invalidCandidate(source Source) error {
	return &xerror.OperationError{
		Operation: "select_adb_executable",
		Detail:    fmt.Sprintf("unusable %s ADB executable", source),
		Err:       fmt.Errorf("%w (%s)", xerror.ErrADBInvalidCandidate, source),
	}
}
