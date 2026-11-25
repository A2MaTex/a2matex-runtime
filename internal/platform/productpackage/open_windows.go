package productpackage

import (
	"os"

	"golang.org/x/sys/windows"

	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func openPackageFile(root *os.Root, reference string) (*os.File, error) {
	file, err := root.OpenFile(reference, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	kind, err := windows.GetFileType(windows.Handle(file.Fd()))
	if err != nil || kind != windows.FILE_TYPE_DISK {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, xerror.ErrProductPackageInvalid
	}
	return file, nil
}
