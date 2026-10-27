//go:build !linux && !windows

package discovery

import xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"

func resolveConnectionDirectory() (string, error) {
	return "", xerror.ErrResolveLocalAuthDirectory
}
