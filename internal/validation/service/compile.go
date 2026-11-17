package service

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/editorgraph"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type CompilationResult struct {
	Validation validation.Result `json:"validation"`
	RuntimeIR  json.RawMessage   `json:"runtimeIr"`
}

const maxIRNodeFields = constants.RuntimeIRMaxFields

var runtimeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func (s *Service) Compile(rawGraph, rawInputSchema []byte) (CompilationResult, error) {
	if len(rawGraph) > validation.EditorGraphMaxContentBytes {
		return CompilationResult{}, ErrPayloadTooLarge
	}
	graph, diagnostics := editorgraph.Decode(rawGraph)
	inputs, inputDiagnostics := validateInputSchema(rawInputSchema)
	diagnostics = append(diagnostics, inputDiagnostics...)
	if graph != nil && len(inputDiagnostics) == 0 {
		diagnostics = s.validateGraph(graph, diagnostics, inputs)
		if len(diagnostics) == 0 {
			diagnostics = validateIRSource(graph)
		}
	}

	result := CompilationResult{Validation: validation.BuildResult(diagnostics, nil)}
	if result.Validation.Status == validation.StatusInvalid {
		return result, nil
	}

	ir, err := s.buildIR(graph, rawInputSchema)
	if err != nil {
		return CompilationResult{}, xerror.WithOperation("build_runtime_ir", err)
	}
	encoded, err := json.Marshal(ir)
	if err != nil {
		return CompilationResult{}, xerror.WithOperation("encode_runtime_ir", err)
	}
	if len(encoded) > constants.RuntimeIRMaxContentBytes {
		result.Validation = validation.BuildResult([]validation.Diagnostic{
			validation.NewDiagnostic(validation.CodeRuntimeIRPayloadTooLarge, validation.Path{}, "", ""),
		}, nil)
		return result, nil
	}
	result.RuntimeIR = encoded
	return result, nil
}

func validateIRSource(graph *editorgraph.Graph) []validation.Diagnostic {
	var diagnostics []validation.Diagnostic
	check := func(value string, path validation.Path) {
		if !runtimeID.MatchString(value) {
			diagnostics = append(diagnostics, validation.NewDiagnostic(validation.CodeEditorGraphContractInvalid, path, "", ""))
		}
	}
	for index, node := range graph.Nodes {
		check(node.ID, validation.Path{"nodes", index, "id"})
		if len(node.Config) > maxIRNodeFields || len(node.Bindings) > maxIRNodeFields {
			diagnostics = append(diagnostics, validation.NewDiagnostic(
				validation.CodeEditorGraphContractInvalid, validation.Path{"nodes", index}, "", ""))
		}
	}
	for index, edge := range graph.Edges {
		check(edge.ID, validation.Path{"edges", index, "id"})
		check(edge.SourcePort, validation.Path{"edges", index, "sourcePort"})
	}
	return diagnostics
}

func (s *Service) buildIR(graph *editorgraph.Graph, inputSchema json.RawMessage) (ir.Document, error) {
	document := ir.Document{
		FormatVersion:         constants.RuntimeIRFormatVersion,
		MinimumRuntimeVersion: "0.0.0",
		Metadata:              ir.Metadata{SourceGraphSchemaVersion: graph.SchemaVersion},
		EntryNodeID:           graph.EntryNodeID,
		InputSchema:           inputSchema,
		Nodes:                 make([]ir.Node, 0, len(graph.Nodes)),
		Edges:                 graph.Edges,
		RequiredCapabilities:  []string{},
	}
	seenCapabilities := make(map[string]bool)
	for _, node := range graph.Nodes {
		definition, found := s.registry.CompilationDefinition(node.Type)
		if !found {
			return ir.Document{}, fmt.Errorf("missing installed compilation definition")
		}
		if versionGreater(definition.MinimumRuntimeVersion, document.MinimumRuntimeVersion) {
			document.MinimumRuntimeVersion = definition.MinimumRuntimeVersion
		}
		for _, capability := range definition.Capabilities {
			if !seenCapabilities[capability] {
				document.RequiredCapabilities = append(document.RequiredCapabilities, capability)
				seenCapabilities[capability] = true
			}
		}

		compiled := ir.Node{
			ID: node.ID, Type: node.Type, RuntimeHandler: definition.RuntimeHandler,
			Config: node.Config, Inputs: make(ir.Object, len(node.Bindings)),
			ErrorPolicy: node.ErrorPolicy, TimeoutMS: definition.DefaultTimeoutMS,
			RetryPolicy: ir.RetryPolicy{MaxAttempts: 1},
		}
		if node.TimeoutMS != nil {
			compiled.TimeoutMS = *node.TimeoutMS
		}
		if node.RetryPolicy != nil {
			if node.RetryPolicy.MaxAttempts != nil {
				compiled.RetryPolicy.MaxAttempts = *node.RetryPolicy.MaxAttempts
			}
			if node.RetryPolicy.DelayMS != nil {
				compiled.RetryPolicy.DelayMS = *node.RetryPolicy.DelayMS
			}
		}
		for name, raw := range node.Bindings {
			var binding editorgraph.JSONObject
			if err := json.Unmarshal(raw, &binding); err != nil || binding["kind"] == nil {
				return ir.Document{}, fmt.Errorf("validated binding could not be compiled")
			}
			binding["source"] = binding["kind"]
			delete(binding, "kind")
			if encoded := binding["nodeId"]; encoded != nil {
				var id string
				if err := json.Unmarshal(encoded, &id); err != nil {
					return ir.Document{}, err
				}
				binding["nodeId"], _ = json.Marshal(editorgraph.TrimIdentifier(id))
			}
			encoded, err := json.Marshal(binding)
			if err != nil {
				return ir.Document{}, err
			}
			compiled.Inputs[name] = encoded
		}
		document.Nodes = append(document.Nodes, compiled)
	}
	return document, nil
}

func versionGreater(left, right string) bool {
	comparison, valid := ir.CompareVersions(left, right)
	return valid && comparison > 0
}
