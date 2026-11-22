package config

import (
	"strings"
	"testing"
)

func TestLoadWithoutExecutionConcurrencySetting(t *testing.T) {
	cases := []struct {
		name, file, env string
		wantErr         bool
	}{
		{name: "default"},
		{name: "removed zero", file: "execution:\n  max_concurrent_devices: 0", wantErr: true},
		{name: "removed positive", file: "execution:\n  max_concurrent_devices: 128", wantErr: true},
		{name: "decimal string", file: "execution:\n  max_concurrent_devices: '08'", wantErr: true},
		{name: "environment only", env: "3"},
		{name: "environment cannot restore removed field", file: "execution:\n  max_concurrent_devices: 1", env: "3", wantErr: true},
		{name: "environment cannot hide removed field", file: "execution:\n  max_concurrent_devices: false", env: "4", wantErr: true},
		{name: "empty environment uses file", file: "execution:\n  max_concurrent_devices: 5", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnvironment(t)
			t.Setenv("A2MATEX_RUNTIME_EXECUTION_MAX_CONCURRENT_DEVICES", tc.env)
			var overrides Overrides
			if tc.file != "" {
				overrides.ConfigFile = writeConfigFile(t, tc.file)
			}
			_, err := Load(overrides)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestLoadRejectsRemovedExecutionSection(t *testing.T) {
	for _, raw := range []string{"-1", "2.5", "2.0", "true", "[]", "{}", "'2.0'", "'2e0'", "'unlimited'", "' '", "999999999999999999999999"} {
		t.Run("yaml/"+raw, func(t *testing.T) {
			clearEnvironment(t)
			_, err := Load(Overrides{ConfigFile: writeConfigFile(t, "execution:\n  max_concurrent_devices: "+raw)})
			if err == nil || !strings.Contains(err.Error(), "execution") {
				t.Fatalf("removed YAML field accepted or misclassified: %v", err)
			}
		})
	}
	for _, raw := range []string{"-1", "2.5", "2.0", "true", "2e0", "unlimited", " ", "999999999999999999999999"} {
		t.Run("environment/"+raw, func(t *testing.T) {
			clearEnvironment(t)
			t.Setenv("A2MATEX_RUNTIME_EXECUTION_MAX_CONCURRENT_DEVICES", raw)
			_, err := Load(Overrides{})
			if err != nil {
				t.Fatalf("obsolete environment variable affected configuration: %v", err)
			}
		})
	}
	for _, file := range []string{"execution: []", "execution: true", "execution:\n  misspelled_limit: 2"} {
		t.Run(file, func(t *testing.T) {
			clearEnvironment(t)
			if _, err := Load(Overrides{ConfigFile: writeConfigFile(t, file)}); err == nil {
				t.Fatal("invalid execution section was hidden by normalization")
			}
		})
	}
}
