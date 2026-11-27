// Package productworkflow loads checked protected definitions without admitting execution.
package productworkflow

import (
	"encoding/json"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
)

// Loaded owns a definition until its caller joins all borrowers and closes it.
// Document is immutable while borrowed. Transfers and Close require one owner.
type Loaded struct {
	Document      *ir.Document `json:"-"`
	RuntimeIRHash string       `json:"-"`
	inputs        []runtimeir.InputValues
}

// TakeInputs transfers live execution state separately from definition cleanup.
func (l *Loaded) TakeInputs() []runtimeir.InputValues {
	if l == nil {
		return nil
	}
	inputs := l.inputs
	l.inputs = nil
	return inputs
}

func (l *Loaded) Close() {
	if l == nil {
		return
	}
	if document := l.Document; document != nil {
		clear(document.InputSchema)
		for i := range document.Nodes {
			clearObject(document.Nodes[i].Config)
			clearObject(document.Nodes[i].Inputs)
		}
		clear(document.Nodes)
		clear(document.Edges)
		clear(document.RequiredCapabilities)
		*document = ir.Document{}
	}
	for _, inputs := range l.inputs {
		clearObject(inputs)
	}
	clear(l.inputs)
	l.Document, l.inputs, l.RuntimeIRHash = nil, nil, ""
}

func clearObject(values map[string]json.RawMessage) {
	for _, raw := range values {
		clear(raw)
	}
	clear(values)
}
