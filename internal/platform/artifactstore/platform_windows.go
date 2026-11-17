package artifactstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func openArtifactDirectory(stateDirectory string) (*os.File, error) {
	// D1 supplies an absolute, canonical local-drive path.
	if !strings.HasPrefix(stateDirectory, `\\?\`) {
		stateDirectory = `\\?\` + stateDirectory
	}
	parent, err := openDirectory(stateDirectory)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	path := filepath.Join(parent.Name(), directoryName)
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	return openDirectory(path)
}

func openDirectory(path string) (*os.File, error) {
	// Excluding delete sharing keeps the checked directory in place while open.
	return openWindowsFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, true)
}

func createEntry(dir *os.File, name string) (*os.File, error) {
	return openWindowsFile(filepath.Join(dir.Name(), name), windows.GENERIC_WRITE, 0,
		windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, false)
}

func openEntry(dir *os.File, name string) (*os.File, error) {
	return openWindowsFile(filepath.Join(dir.Name(), name), windows.GENERIC_READ, windows.FILE_SHARE_READ,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, false)
}

func openWindowsFile(path string, access, sharing, disposition, flags uint32, directory bool) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, access, sharing, nil, disposition,
		flags|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	kind, typeErr := windows.GetFileType(handle)
	infoErr := windows.GetFileInformationByHandle(handle, &info)
	if typeErr != nil || infoErr != nil || kind != windows.FILE_TYPE_DISK ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		(info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory ||
		(!directory && info.NumberOfLinks != 1) {
		_ = windows.CloseHandle(handle)
		return nil, errUnsafeEntry
	}
	return os.NewFile(uintptr(handle), path), nil
}

func publishEntry(dir *os.File, from, to string) error {
	source, err := windows.UTF16PtrFromString(filepath.Join(dir.Name(), from))
	if err != nil {
		return err
	}
	destination, err := windows.UTF16PtrFromString(filepath.Join(dir.Name(), to))
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source, destination, windows.MOVEFILE_WRITE_THROUGH)
}

// MoveFileEx uses write-through on Windows.
func syncDirectory(_ *os.File) error { return nil }

func removeEntry(dir *os.File, name string) error {
	file, err := openEntry(dir, name)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir.Name(), name))
}
