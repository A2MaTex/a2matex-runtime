//go:build !linux && !windows

package sqlitestore

import (
	"errors"
	"os"
)

func stateDirectory() (string, error) {
	return "", errors.New("execution storage is unsupported on this platform")
}

func lockExclusive(_ *os.File) error {
	return errors.New("execution store locking is unsupported on this platform")
}

func checkFilesystem(_ string) error {
	return errors.New("execution storage is unsupported on this platform")
}
