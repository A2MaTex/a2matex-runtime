package execution

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestEncodeVariableScalar(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, input, want string }{
		{"false is present", " false ", "false"}, {"zero", "0", "0"},
		{"empty string", `""`, `""`}, {"compact number", "1.0", "1"},
		{"negative zero", "-0", "-0"}, {"large finite", "1e300", "1e+300"},
		{"unicode", `"😀"`, `"😀"`}, {"escape normalization", `"\u0061"`, `"a"`},
		{"owned string", `"value"`, `"value"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.input)
			got, err := EncodeVariableScalar(raw)
			if err != nil || string(got) != tc.want {
				t.Fatalf("got %s, %v; want %s", got, err, tc.want)
			}
			for i := range raw {
				raw[i] = 'x'
			}
			if string(got) != tc.want {
				t.Fatal("codec retained caller bytes")
			}
		})
	}
	for _, tc := range []struct{ name, input string }{
		{"missing", ""}, {"null", "null"}, {"array", "[]"}, {"object", "{}"},
		{"trailing json", "true false"}, {"invalid number", "NaN"}, {"overflow", "1e400"},
		{"invalid utf8", string([]byte{'"', 0xff, '"'})},
		{"raw too large", `"` + strings.Repeat("s", constants.MaxVariableValueBytes-1) + `"`},
		{"encoded too large", `"` + strings.Repeat("<", constants.MaxVariableValueBytes/6+1) + `"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EncodeVariableScalar([]byte(tc.input))
			var command xerror.CommandError
			if got != nil || !errors.As(err, &command) || command.Code != constants.CommandInputUnsupported || command.Retryable {
				t.Fatalf("invalid scalar result: %s %v", got, err)
			}
			if strings.Contains(err.Error(), "ssss") || strings.Contains(err.Error(), "<<<<") {
				t.Fatal("error disclosed a value")
			}
		})
	}
	raw := []byte(`"` + strings.Repeat("x", constants.MaxVariableValueBytes-2) + `"`)
	got, err := EncodeVariableScalar(raw)
	if err != nil || len(got) != constants.MaxVariableValueBytes || !bytes.Equal(got, raw) {
		t.Fatal("exact value limit rejected", err)
	}
}
