package service

import (
	"context"
	"encoding/json"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
)

func validateInputSchema(raw json.RawMessage) (*nodecontract.WorkflowInputs, []validation.Diagnostic) {
	var diagnostics []validation.Diagnostic
	inputs, _ := nodecontract.DecodeWorkflowInputs(context.Background(), raw, validation.Path{"inputSchema"}, func(finding validation.Diagnostic) {
		diagnostics = append(diagnostics, finding)
	})
	return inputs, diagnostics
}
