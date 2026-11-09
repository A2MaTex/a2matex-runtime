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
		return "", errors.New("resolve Runtime state directory")
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
		return errors.New("execution store requires a local drive")
	}
	path, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil {
		return errors.New("inspect execution store drive")
	}
	switch windows.GetDriveType(path) {
	case windows.DRIVE_FIXED, windows.DRIVE_REMOVABLE:
		return nil
	default:
		return errors.New("execution store requires a supported local drive")
	}
}
