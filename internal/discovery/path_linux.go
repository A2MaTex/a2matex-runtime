//go:build linux

package discovery

import (
	"os"
	"path/filepath"

	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func resolveConnectionDirectory() (string, error) {
	return resolveLinuxConnectionDirectory(os.Getenv, os.UserHomeDir)
}

func resolveLinuxConnectionDirectory(
	getenv func(string) string,
	userHomeDir func() (string, error),
) (string, error) {
	// Prefer the per-login runtime directory because the OS normally removes it
	// after logout. The config directory is the fallback when it is unavailable.
	if runtimeDirectory := getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(runtimeDirectory) {
		if info, err := os.Stat(runtimeDirectory); err == nil && info.IsDir() {
			return filepath.Join(runtimeDirectory, "a2matex"), nil
		}
	}

	if configDirectory := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(configDirectory) {
		return filepath.Join(configDirectory, "a2matex", "runtime"), nil
	}

	// XDG_CONFIG_HOME is optional, so Linux defaults to $HOME/.config.
	homeDirectory, err := userHomeDir()
	if err != nil || !filepath.IsAbs(homeDirectory) {
		return "", xerror.ErrResolveLocalAuthDirectory
	}

	return filepath.Join(homeDirectory, ".config", "a2matex", "runtime"), nil
}
