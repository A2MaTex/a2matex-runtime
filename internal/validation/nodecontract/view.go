package nodecontract

import (
	"context"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/editorgraph"
)

// Views carry only executable semantics into the shared node checks.
type sourceForm struct {
	source string
	inputs string
	root   validation.Path
}

type nodeView struct {
	ID          string
	Type        string
	Config      ir.Object
	Inputs      ir.Object
	ErrorPolicy string
	form        sourceForm
}

type graphView struct {
	EntryNodeID string
	Nodes       []nodeView
	Edges       []ir.Edge
	ctx         context.Context
	dominance   bool
}

func Validate(graph *editorgraph.Graph, registry *Registry) []validation.Diagnostic {
	return ValidateWithInputs(graph, registry, nil)
}

func ValidateWithInputs(graph *editorgraph.Graph, registry *Registry, inputs *WorkflowInputs) []validation.Diagnostic {
	view := graphView{EntryNodeID: graph.EntryNodeID, Edges: graph.Edges, ctx: context.Background(), dominance: true}
	form := sourceForm{source: "kind", inputs: "bindings"}
	for _, node := range graph.Nodes {
		view.Nodes = append(view.Nodes, nodeView{ID: node.ID, Type: node.Type, Config: node.Config,
			Inputs: node.Bindings, ErrorPolicy: node.ErrorPolicy, form: form})
	}
	var diagnostics []validation.Diagnostic
	_ = validateView(&view, registry, inputs, func(finding validation.Diagnostic) {
		diagnostics = append(diagnostics, finding)
	})
	return diagnostics
}

// ValidateIR reuses the installed node checks with executable sources and request-relative paths.
func (r *Registry) ValidateIR(ctx context.Context, document *ir.Document, inputs *WorkflowInputs, dominance bool, report func(validation.Diagnostic)) error {
	view := graphView{EntryNodeID: document.EntryNodeID, Edges: document.Edges, ctx: ctx, dominance: dominance}
	form := sourceForm{source: "source", inputs: "inputs", root: validation.Path{"runtimeIr"}}
	for _, node := range document.Nodes {
		view.Nodes = append(view.Nodes, nodeView{ID: node.ID, Type: node.Type, Config: node.Config,
			Inputs: node.Inputs, ErrorPolicy: node.ErrorPolicy, form: form})
	}
	return validateView(&view, r, inputs, report)
}

func (n nodeView) path(index int, parts ...any) validation.Path {
	path := append(validation.Path{}, n.form.root...)
	path = append(path, "nodes", index)
	return append(path, parts...)
}

func (n nodeView) inputPath(index int, parts ...any) validation.Path {
	return append(n.path(index, n.form.inputs), parts...)
}

func (n nodeView) configPath(index int, field string) validation.Path {
	return n.path(index, "config", field)
}

func (n nodeView) configDiagnostic(index int, field string) validation.Diagnostic {
	return validation.NewDiagnostic(validation.CodeNodeConfigInvalid, n.configPath(index, field), n.ID, "")
}

func (n nodeView) portPath(index int, port string) validation.Path {
	if n.form.source == "source" {
		return n.path(index)
	}
	return n.path(index, "ports", port)
}

func (g *graphView) edgePath(index int, field string) validation.Path {
	path := validation.Path{}
	if len(g.Nodes) > 0 {
		path = append(path, g.Nodes[0].form.root...)
	}
	return append(path, "edges", index, field)
}
