package service

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/editorgraph"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type CompilationResult struct {
	Validation validation.Result `json:"validation"`
	RuntimeIR  json.RawMessage   `json:"runtimeIr"`
}

type runtimeIR struct {
	FormatVersion         int                `json:"formatVersion"`
	MinimumRuntimeVersion string             `json:"minimumRuntimeVersion"`
	Metadata              sourceMetadata     `json:"metadata"`
	EntryNodeID           string             `json:"entryNodeId"`
	InputSchema           json.RawMessage    `json:"inputSchema"`
	Nodes                 []runtimeNode      `json:"nodes"`
	Edges                 []editorgraph.Edge `json:"edges"`
	RequiredCapabilities  []string           `json:"requiredCapabilities"`
}

type sourceMetadata struct {
	SourceGraphSchemaVersion int64 `json:"sourceGraphSchemaVersion"`
}

type runtimeNode struct {
	ID             string                            `json:"id"`
	Type           string                            `json:"type"`
	RuntimeHandler string                            `json:"runtimeHandler"`
	Config         editorgraph.JSONObject            `json:"config"`
	Inputs         map[string]editorgraph.JSONObject `json:"inputs"`
	TimeoutMS      int64                             `json:"timeoutMs"`
	RetryPolicy    runtimeRetry                      `json:"retryPolicy"`
	ErrorPolicy    editorgraph.ErrorPolicy           `json:"errorPolicy"`
}

type runtimeRetry struct {
	MaxAttempts int64 `json:"maxAttempts"`
	DelayMS     int64 `json:"delayMs"`
}

const maxIRNodeFields = 100

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

func (s *Service) buildIR(graph *editorgraph.Graph, inputSchema json.RawMessage) (runtimeIR, error) {
	ir := runtimeIR{
		FormatVersion:         constants.RuntimeIRFormatVersion,
		MinimumRuntimeVersion: "0.0.0",
		Metadata:              sourceMetadata{SourceGraphSchemaVersion: graph.SchemaVersion},
		EntryNodeID:           graph.EntryNodeID,
		InputSchema:           inputSchema,
		Nodes:                 make([]runtimeNode, 0, len(graph.Nodes)),
		Edges:                 graph.Edges,
		RequiredCapabilities:  []string{},
	}
	seenCapabilities := make(map[string]bool)
	for _, node := range graph.Nodes {
		definition, found := s.registry.CompilationDefinition(node.Type)
		if !found {
			return runtimeIR{}, fmt.Errorf("missing installed compilation definition")
		}
		if versionGreater(definition.MinimumRuntimeVersion, ir.MinimumRuntimeVersion) {
			ir.MinimumRuntimeVersion = definition.MinimumRuntimeVersion
		}
		for _, capability := range definition.Capabilities {
			if !seenCapabilities[capability] {
				ir.RequiredCapabilities = append(ir.RequiredCapabilities, capability)
				seenCapabilities[capability] = true
			}
		}

		compiled := runtimeNode{
			ID: node.ID, Type: node.Type, RuntimeHandler: definition.RuntimeHandler,
			Config: node.Config, Inputs: make(map[string]editorgraph.JSONObject, len(node.Bindings)),
			ErrorPolicy: node.ErrorPolicy, TimeoutMS: definition.DefaultTimeoutMS,
			RetryPolicy: runtimeRetry{MaxAttempts: 1},
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
				return runtimeIR{}, fmt.Errorf("validated binding could not be compiled")
			}
			binding["source"] = binding["kind"]
			delete(binding, "kind")
			if encoded := binding["nodeId"]; encoded != nil {
				var id string
				if err := json.Unmarshal(encoded, &id); err != nil {
					return runtimeIR{}, err
				}
				binding["nodeId"], _ = json.Marshal(editorgraph.TrimIdentifier(id))
			}
			compiled.Inputs[name] = binding
		}
		ir.Nodes = append(ir.Nodes, compiled)
	}
	return ir, nil
}

func versionGreater(left, right string) bool {
	leftFields, rightFields := strings.Split(left, "."), strings.Split(right, ".")
	for index, part := range leftFields {
		other := rightFields[index]
		if len(part) != len(other) {
			return len(part) > len(other)
		}
		if part != other {
			return part > other
		}
	}
	return false
}
