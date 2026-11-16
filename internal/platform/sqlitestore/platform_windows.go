package sqlitestore

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func stateDirectory() (string, error) {
	root := os.Getenv("LOCALAPPDATA")
	if !filepath.IsAbs(root) {
		return "", storeFailure("resolve_state_directory", "resolve Runtime state directory", nil)
	}
	return filepath.Join(root, "A2MaTeX", "Runtime"), nil
}

func lockExclusive(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|
		windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}

func checkFilesystem(root string) error {
	volume := filepath.VolumeName(root)
	if len(volume) != 2 || volume[1] != ':' {
		return storeFailure("check_store_filesystem", "execution store requires a local drive", nil)
	}
	path, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil {
		return storeFailure("inspect_store_filesystem", "inspect execution store drive", err)
	}
	switch windows.GetDriveType(path) {
	case windows.DRIVE_FIXED, windows.DRIVE_REMOVABLE:
		return nil
	default:
		return storeFailure("check_store_filesystem", "execution store requires a supported local drive", nil)
	}
}

func isLockContention(err error) bool { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }
