package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadADBSettings(t *testing.T) {
	file := filepath.Join(t.TempDir(), "runtime.yaml")
	if err := os.WriteFile(file, []byte("adb:\n  allow_path_fallback: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("A2MATEX_RUNTIME_ADB_ALLOW_PATH_FALLBACK", "false")
	binary := filepath.Join(t.TempDir(), "adb.exe")
	t.Setenv("A2MATEX_RUNTIME_ADB_EXECUTABLE_PATH", binary)

	cfg, err := Load(Overrides{ConfigFile: file})
	if err != nil || cfg.ADB != (ADBConfig{ExecutablePath: binary}) {
		t.Fatalf("ADB configuration = %+v, %v", cfg.ADB, err)
	}

	t.Setenv("A2MATEX_RUNTIME_ADB_EXECUTABLE_PATH", "relative/adb.exe")
	if _, err := Load(Overrides{}); err == nil {
		t.Fatal("relative override accepted")
	}
}

func TestLoadRejectsInvalidADBTypes(t *testing.T) {
	for _, setting := range []string{"executable_path: 42", `allow_path_fallback: "true"`} {
		t.Run(setting, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "runtime.yaml")
			if err := os.WriteFile(file, []byte("adb:\n  "+setting+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(Overrides{ConfigFile: file}); err == nil {
				t.Fatal("invalid YAML type accepted")
			}
		})
	}
}
