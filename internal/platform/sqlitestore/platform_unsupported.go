//go:build !linux && !windows

package sqlitestore

import "os"

func stateDirectory() (string, error) {
	return "", storeFailure("resolve_state_directory", "execution storage is unsupported on this platform", nil)
}

func lockExclusive(_ *os.File) error {
	return storeFailure("acquire_store_lock", "execution store locking is unsupported on this platform", nil)
}

func checkFilesystem(_ string) error {
	return storeFailure("check_store_filesystem", "execution storage is unsupported on this platform", nil)
}

func isLockContention(err error) bool { return false }
