package sqlitestore

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func stateDirectory() (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(root) {
		home, err := os.UserHomeDir()
		if err != nil || !filepath.IsAbs(home) {
			return "", storeFailure("resolve_state_directory", "resolve Runtime state directory", err)
		}
		root = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(root, "a2matex", "runtime"), nil
}

func lockExclusive(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func checkFilesystem(root string) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(root, &stat); err != nil {
		return storeFailure("inspect_store_filesystem", "inspect execution store filesystem", err)
	}
	switch uint32(stat.Type) {
	case unix.NFS_SUPER_MAGIC, unix.CIFS_SUPER_MAGIC, unix.SMB_SUPER_MAGIC, unix.V9FS_MAGIC:
		return storeFailure("check_store_filesystem", "execution store requires a supported local filesystem", nil)
	}
	return nil
}

func isLockContention(err error) bool { return errors.Is(err, unix.EWOULDBLOCK) }
