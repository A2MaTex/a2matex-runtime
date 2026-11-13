package service

import (
	"errors"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/editorgraph"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/topology"
)

var ErrPayloadTooLarge = errors.New("editor graph payload is too large")

type Service struct {
	registry *nodecontract.Registry
}

func New(registry *nodecontract.Registry) *Service {
	return &Service{registry: registry}
}

func (s *Service) Validate(raw []byte) (validation.Result, error) {
	if len(raw) > validation.EditorGraphMaxContentBytes {
		return validation.Result{}, ErrPayloadTooLarge
	}
	graph, decodeDiagnostics := editorgraph.Decode(raw)
	if graph == nil {
		return validation.BuildResult(decodeDiagnostics, nil), nil
	}

	diagnostics := s.validateGraph(graph, decodeDiagnostics, nil)
	return validation.BuildResult(diagnostics, nil), nil
}

func (s *Service) validateGraph(graph *editorgraph.Graph, decodeDiagnostics []validation.Diagnostic, inputs *nodecontract.WorkflowInputs) []validation.Diagnostic {
	topologyResult := topology.Validate(graph)
	diagnostics := append(decodeDiagnostics, topologyResult.Diagnostics...)
	if topologyResult.Continue {
		diagnostics = append(diagnostics, nodecontract.ValidateWithInputs(graph, s.registry, inputs)...)
	}
	return diagnostics
}
