//go:build windows

package discovery

import (
	"os"
	"path/filepath"

	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func resolveConnectionDirectory() (string, error) {
	// LOCALAPPDATA is scoped to the signed-in Windows user, matching the
	// per-user ownership of the session token in the connection document.
	localAppData := os.Getenv("LOCALAPPDATA")
	if !filepath.IsAbs(localAppData) {
		return "", xerror.ErrResolveLocalAuthDirectory
	}

	return filepath.Join(localAppData, "A2MaTeX", "Runtime"), nil
}
