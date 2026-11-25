//go:build !linux && !windows

package productpackage

import (
	"os"

	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func openPackageFile(_ *os.Root, _ string) (*os.File, error) {
	return nil, xerror.ErrProductPackageUnavailable
}
