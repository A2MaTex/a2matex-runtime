package execution

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// VariableWrite is a RAM candidate, never a persistence DTO.
type VariableWrite struct {
	AttemptRef
	Scope constants.VariableScope
	Name  string
	Value json.RawMessage
}

// EncodeVariableScalar returns owned compact JSON using the existing scalar types.
func EncodeVariableScalar(raw json.RawMessage) (json.RawMessage, error) {
	fail := func() (json.RawMessage, error) {
		return nil, xerror.WithOperation("encode_scoped_variable", xerror.CommandError{
			Code: constants.CommandInputUnsupported, Message: "Variable value must be a finite scalar within the size limit",
		})
	}
	if len(raw) > constants.MaxVariableValueBytes || !utf8.Valid(raw) {
		return fail()
	}
	if _, valid := nodecontract.ScalarType(raw); !valid {
		return fail()
	}
	var scalar any
	if err := json.Unmarshal(raw, &scalar); err != nil {
		return fail()
	}
	encoded, err := json.Marshal(scalar)
	if err != nil || len(encoded) > constants.MaxVariableValueBytes {
		return fail()
	}
	return encoded, nil
}
