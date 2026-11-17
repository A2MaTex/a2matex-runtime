package runtimeir

import (
	"slices"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
)

func (v *Validator) compatibility(c *checks, document *ir.Document) {
	minimum := "0.0.0"
	capabilities := []string{}
	seen := make(map[string]bool)
	allKnown := true
	for index, node := range document.Nodes {
		if c.ctx.Err() != nil {
			return
		}
		definition, known := v.definitions.CompilationDefinition(node.Type)
		if !known {
			allKnown = false
			continue
		}
		if node.RuntimeHandler != definition.RuntimeHandler {
			c.add(validation.NewDiagnostic(validation.CodeNodeRuntimeHandlerInvalid,
				validation.Path{"runtimeIr", "nodes", index, "runtimeHandler"}, node.ID, ""))
		} else if _, enabled := v.handlers.Lookup(node.Type, node.RuntimeHandler); !enabled {
			c.add(validation.NewDiagnostic(validation.CodeNodeRuntimeHandlerUnavailable,
				validation.Path{"runtimeIr", "nodes", index, "runtimeHandler"}, node.ID, ""))
		}
		comparison, _ := ir.CompareVersions(definition.MinimumRuntimeVersion, minimum)
		if comparison > 0 {
			minimum = definition.MinimumRuntimeVersion
		}
		for _, capability := range definition.Capabilities {
			if !seen[capability] {
				seen[capability] = true
				capabilities = append(capabilities, capability)
			}
		}
	}
	if !allKnown {
		return
	}
	if !slices.Equal(capabilities, document.RequiredCapabilities) {
		c.add(validation.NewDiagnostic(validation.CodeRuntimeIRCapabilitiesInvalid, validation.Path{"runtimeIr", "requiredCapabilities"}, "", ""))
	}
	if document.MinimumRuntimeVersion != minimum {
		c.add(validation.NewDiagnostic(validation.CodeRuntimeIRMinimumVersionInvalid, validation.Path{"runtimeIr", "minimumRuntimeVersion"}, "", ""))
	}
	comparison, valid := ir.CompareVersions(v.runtimeVersion, minimum)
	if !valid || comparison < 0 {
		c.add(validation.NewDiagnostic(validation.CodeRuntimeVersionUnsupported, validation.Path{"runtimeIr", "minimumRuntimeVersion"}, "", ""))
	}
}
