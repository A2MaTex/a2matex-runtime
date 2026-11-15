package config

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
)

type ADBConfig struct {
	ExecutablePath    string `mapstructure:"executable_path"`
	AllowPathFallback bool   `mapstructure:"allow_path_fallback"`
}

func (c ADBConfig) Validate() error {
	if c.ExecutablePath == "" {
		return nil
	}
	if !filepath.IsAbs(c.ExecutablePath) || strings.ContainsRune(c.ExecutablePath, 0) {
		return errors.New("adb.executable_path must be an absolute file path")
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(c.ExecutablePath), ".exe") {
		return errors.New("adb.executable_path must name an .exe on Windows")
	}
	return nil
}
