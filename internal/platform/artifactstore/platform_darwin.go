// macOS (development) support: same POSIX implementation as platform_linux.go.

package artifactstore

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openArtifactDirectory(stateDirectory string) (*os.File, error) {
	const directoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	parentFD, err := unix.Open(stateDirectory, directoryFlags, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parentFD)
	if err := unix.Mkdirat(parentFD, directoryName, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
		return nil, err
	}
	fd, err := unix.Openat(parentFD, directoryName, directoryFlags, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), directoryName)
	info, err := dir.Stat()
	if err != nil || info.Mode().Perm()&0077 != 0 {
		_ = dir.Close()
		return nil, errUnsafeEntry
	}
	if err := unix.Fsync(parentFD); err != nil {
		_ = dir.Close()
		return nil, err
	}
	return dir, nil
}

func createEntry(dir *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func openEntry(dir *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil || info.Mode&unix.S_IFMT != unix.S_IFREG || info.Nlink != 1 {
		_ = unix.Close(fd)
		return nil, errUnsafeEntry
	}
	return os.NewFile(uintptr(fd), name), nil
}

func publishEntry(dir *os.File, from, to string) error {
	fd := int(dir.Fd())
	return unix.RenameatxNp(fd, from, fd, to, unix.RENAME_EXCL)
}

func syncDirectory(dir *os.File) error { return dir.Sync() }

func removeEntry(dir *os.File, name string) error {
	fd := int(dir.Fd())
	var info unix.Stat_t
	if err := unix.Fstatat(fd, name, &info, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Nlink != 1 {
		return errUnsafeEntry
	}
	return unix.Unlinkat(fd, name, 0)
}
