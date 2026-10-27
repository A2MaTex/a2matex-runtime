//go:build linux

package discovery

import (
	"errors"
	"path/filepath"
	"testing"

	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestResolveLinuxConnectionDirectory(t *testing.T) {
	t.Parallel()
	runtimeDirectory := t.TempDir()
	configDirectory := t.TempDir()

	tests := []struct {
		name    string
		env     map[string]string
		home    string
		homeErr error
		want    string
		wantErr bool
	}{
		{
			name: "runtime directory",
			env: map[string]string{
				"XDG_RUNTIME_DIR": runtimeDirectory,
				"XDG_CONFIG_HOME": configDirectory,
			},
			want: filepath.Join(runtimeDirectory, "a2matex"),
		},
		{
			name: "config directory",
			env: map[string]string{
				"XDG_RUNTIME_DIR": "relative",
				"XDG_CONFIG_HOME": configDirectory,
			},
			want: filepath.Join(configDirectory, "a2matex", "runtime"),
		},
		{
			name: "home fallback",
			env:  map[string]string{},
			home: "/home/runtime-user",
			want: "/home/runtime-user/.config/a2matex/runtime",
		},
		{
			name:    "unavailable home",
			env:     map[string]string{},
			homeErr: errors.New("home unavailable"),
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolveLinuxConnectionDirectory(
				func(key string) string { return test.env[key] },
				func() (string, error) { return test.home, test.homeErr },
			)
			if test.wantErr {
				if !errors.Is(err, xerror.ErrResolveLocalAuthDirectory) {
					t.Fatalf("resolveLinuxConnectionDirectory() error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveLinuxConnectionDirectory() error = %v", err)
			}
			if got != filepath.Clean(test.want) {
				t.Fatalf("resolveLinuxConnectionDirectory() = %q, want %q", got, test.want)
			}
		})
	}
}
