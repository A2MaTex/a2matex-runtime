// macOS (development) support: lets the Runtime run on a Mac with Android SDK emulators.

package sqlitestore

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func stateDirectory() (string, error) {
	// Honour XDG_STATE_HOME like Linux, so tests (and side-by-side instances) get their own store.
	if root := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(root) {
		return filepath.Join(root, "a2matex", "runtime"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", storeFailure("resolve_state_directory", "resolve Runtime state directory", err)
	}
	return filepath.Join(home, "Library", "Application Support", "a2matex", "runtime"), nil
}

func lockExclusive(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func checkFilesystem(root string) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(root, &stat); err != nil {
		return storeFailure("inspect_store_filesystem", "inspect execution store filesystem", err)
	}
	// Same rule as Linux: SQLite and flock are unreliable on network filesystems.
	switch unix.ByteSliceToString(stat.Fstypename[:]) {
	case "nfs", "smbfs", "afpfs", "webdav", "cifs":
		return storeFailure("check_store_filesystem", "execution store requires a supported local filesystem", nil)
	}
	return nil
}

func isLockContention(err error) bool { return errors.Is(err, unix.EWOULDBLOCK) }
