package tap

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestTapWithControlledADB(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "adb")
	const script = `#!/bin/sh
if [ "$#" -eq 1 ] && [ "$1" = version ]; then
    printf 'Android Debug Bridge version 1.0.41\n'
    exit 0
fi
[ "$1" = -s ] && [ "$2" = '192.0.2.5:5555' ] || exit 1
shift 2
printf '%s\n' "$*" >> "$0.calls"
case "$*" in
    'shell -T dumpsys window displays') printf 'Display: mDisplayId=0\n init=1080x1920 cur=1080x1920\n' ;;
    'exec-out cat '*) cat "$0.xml" ;;
    'shell -T rm -f '*) exit 1 ;;
    'shell -T mkdir -m 700 '*|'shell -T uiautomator dump '*|'shell -T rmdir '*|'shell -T input tap '*) exit 0 ;;
    *) exit 1 ;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	device := adb.NewDispatcher(adb.NewResolver(config.ADBConfig{ExecutablePath: binary}))
	const serial = "192.0.2.5:5555"
	const selector = `{"selector_id":"example:id/view"}`
	const validXML = `<hierarchy><node resource-id="example:id/view" bounds="[-40,20][120,100]"/></hierarchy>`
	var lastPath string
	for _, test := range []struct {
		name, mode, inputs, xml string
		wantTap                 string
		wantError               bool
	}{
		{"pixel", constants.AndroidTargetPixels, `{"x":12,"y":34}`, "", "shell -T input tap 12 34", false},
		{"percent", constants.AndroidTargetPercent, `{"xPercent":50,"yPercent":25}`, "", "shell -T input tap 540 480", false},
		{"selector beyond E1 default", constants.AndroidTargetSelector, selector,
			strings.Repeat(" ", constants.ProcessOutputLimitBytes+1) + validXML, "shell -T input tap 60 60", false},
		{"malformed selector XML", constants.AndroidTargetSelector, selector, "<hierarchy>", "", true},
		{"oversized selector XML", constants.AndroidTargetSelector, selector, validXML + strings.Repeat(" ", constants.AndroidHierarchyMaxBytes), "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			for suffix, data := range map[string]string{".calls": "", ".xml": test.xml} {
				if err := os.WriteFile(binary+suffix, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			out, err := runTap(t, device, serial, test.mode, test.inputs)
			if test.wantError {
				var failure xerror.CommandError
				if out != nil || !errors.As(err, &failure) || failure.Code != constants.CommandFailed || failure.Retryable {
					t.Fatalf("output=%#v, error=%#v, want terminal command failure", out, err)
				}
			} else if err != nil || out == nil || len(out) != 0 {
				t.Fatalf("output=%#v, error=%v", out, err)
			}
			actual, err := os.ReadFile(binary + ".calls")
			if err != nil {
				t.Fatal(err)
			}
			calls := strings.Split(strings.TrimSuffix(string(actual), "\n"), "\n")
			var want []string
			if test.mode != constants.AndroidTargetPixels {
				want = append(want, "shell -T dumpsys window displays")
			}
			if test.mode == constants.AndroidTargetSelector {
				if len(calls) < 2 {
					t.Fatalf("missing hierarchy setup: %q", calls)
				}
				const mkdir = "shell -T mkdir -m 700 "
				path := strings.TrimPrefix(calls[1], mkdir)
				if !regexp.MustCompile(`^/data/local/tmp/a2matex-[A-Z2-7]+$`).MatchString(path) || path == lastPath {
					t.Fatalf("unexpected or reused hierarchy path: %q", path)
				}
				lastPath = path
				file := path + "/window.xml"
				want = append(want, mkdir+path, "shell -T uiautomator dump "+file, "exec-out cat "+file,
					"shell -T rm -f "+file, "shell -T rmdir "+path)
			}
			if test.wantTap != "" {
				want = append(want, test.wantTap)
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls = %q, want %q", calls, want)
			}
		})
	}
}
