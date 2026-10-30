package editorgraph

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
)

const maxSafeInteger = int64(9_007_199_254_740_991)

var nodeTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+$`)

func Decode(raw []byte) (*Graph, []validation.Diagnostic) {
	if !utf8.Valid(raw) {
		return nil, contractResult(validation.Path{})
	}

	root, ok := decodeObject(raw)
	if !ok {
		return nil, contractResult(validation.Path{})
	}

	var diagnostics []validation.Diagnostic
	unknownFields(root, []string{"schemaVersion", "entryNodeId", "nodes", "edges"}, validation.Path{}, &diagnostics)

	version, versionOK := decodeSafePositiveInteger(root["schemaVersion"])
	if !versionOK {
		diagnostics = append(diagnostics, contractDiagnostic(validation.Path{"schemaVersion"}))
	}
	entryNodeID, entryOK := decodeIdentifier(root["entryNodeId"])
	if !entryOK {
		diagnostics = append(diagnostics, contractDiagnostic(validation.Path{"entryNodeId"}))
	}
	nodes, nodesOK := decodeNodes(root["nodes"], &diagnostics)
	edges, edgesOK := decodeEdges(root["edges"], &diagnostics)

	if len(diagnostics) > 0 || !versionOK || !entryOK || !nodesOK || !edgesOK {
		return nil, diagnostics
	}

	graph := &Graph{SchemaVersion: version, EntryNodeID: entryNodeID, Nodes: nodes, Edges: edges}
	if version != validation.EditorGraphSchemaVersion {
		return graph, []validation.Diagnostic{validation.NewDiagnostic(
			validation.CodeGraphSchemaVersionUnsupported,
			validation.Path{"schemaVersion"},
			"",
			"",
		)}
	}
	return graph, nil
}

func decodeNodes(raw json.RawMessage, diagnostics *[]validation.Diagnostic) ([]Node, bool) {
	var values []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &values) != nil || values == nil {
		*diagnostics = append(*diagnostics, contractDiagnostic(validation.Path{"nodes"}))
		return nil, false
	}
	nodes := make([]Node, 0, len(values))
	valid := true
	for index, value := range values {
		node, ok := decodeNode(value, index, diagnostics)
		valid = valid && ok
		nodes = append(nodes, node)
	}
	return nodes, valid
}

func decodeNode(raw json.RawMessage, index int, diagnostics *[]validation.Diagnostic) (Node, bool) {
	base := validation.Path{"nodes", index}
	object, ok := decodeObject(raw)
	if !ok {
		*diagnostics = append(*diagnostics, contractDiagnostic(base))
		return Node{}, false
	}
	unknownFields(object, []string{"id", "type", "config", "bindings", "errorPolicy", "editor"}, base, diagnostics)
	valid := true

	id, ok := decodeIdentifier(object["id"])
	if !ok {
		*diagnostics = append(*diagnostics, contractDiagnostic(appendPath(base, "id")))
		valid = false
	}
	nodeType, ok := decodeNodeType(object["type"])
	if !ok {
		*diagnostics = append(*diagnostics, contractDiagnostic(appendPath(base, "type")))
		valid = false
	}
	config, ok := decodeJSONObject(object["config"])
	if !ok {
		*diagnostics = append(*diagnostics, contractDiagnostic(appendPath(base, "config")))
		valid = false
	}
	bindings, ok := decodeJSONObject(object["bindings"])
	if !ok {
		*diagnostics = append(*diagnostics, contractDiagnostic(appendPath(base, "bindings")))
		valid = false
	}
	policy, ok := decodeErrorPolicy(object["errorPolicy"])
	if !ok {
		*diagnostics = append(*diagnostics, contractDiagnostic(appendPath(base, "errorPolicy")))
		valid = false
	}
	position, ok := decodePosition(object["editor"], appendPath(base, "editor"), diagnostics)
	if !ok {
		valid = false
	}

	return Node{ID: id, Type: nodeType, Config: config, Bindings: bindings, ErrorPolicy: policy, Editor: position}, valid
}

func decodeEdges(raw json.RawMessage, diagnostics *[]validation.Diagnostic) ([]Edge, bool) {
	var values []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &values) != nil || values == nil {
		*diagnostics = append(*diagnostics, contractDiagnostic(validation.Path{"edges"}))
		return nil, false
	}
	edges := make([]Edge, 0, len(values))
	valid := true
	for index, rawEdge := range values {
		base := validation.Path{"edges", index}
		object, ok := decodeObject(rawEdge)
		if !ok {
			*diagnostics = append(*diagnostics, contractDiagnostic(base))
			valid = false
			continue
		}
		unknownFields(object, []string{"id", "sourceNodeId", "sourcePort", "targetNodeId"}, base, diagnostics)
		fields := []string{"id", "sourceNodeId", "sourcePort", "targetNodeId"}
		decoded := make([]string, len(fields))
		edgeValid := true
		for fieldIndex, field := range fields {
			decoded[fieldIndex], ok = decodeIdentifier(object[field])
			if !ok {
				*diagnostics = append(*diagnostics, contractDiagnostic(appendPath(base, field)))
				edgeValid = false
			}
		}
		valid = valid && edgeValid
		edges = append(edges, Edge{ID: decoded[0], SourceNodeID: decoded[1], SourcePort: decoded[2], TargetNodeID: decoded[3]})
	}
	return edges, valid
}

func decodePosition(raw json.RawMessage, base validation.Path, diagnostics *[]validation.Diagnostic) (Position, bool) {
	object, ok := decodeObject(raw)
	if !ok {
		*diagnostics = append(*diagnostics, contractDiagnostic(base))
		return Position{}, false
	}
	unknownFields(object, []string{"x", "y"}, base, diagnostics)
	x, xOK := decodeFiniteNumber(object["x"])
	if !xOK {
		*diagnostics = append(*diagnostics, contractDiagnostic(appendPath(base, "x")))
	}
	y, yOK := decodeFiniteNumber(object["y"])
	if !yOK {
		*diagnostics = append(*diagnostics, contractDiagnostic(appendPath(base, "y")))
	}
	return Position{X: x, Y: y}, xOK && yOK
}

func decodeObject(raw []byte) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}

func decodeJSONObject(raw json.RawMessage) (JSONObject, bool) {
	object, ok := decodeObject(raw)
	return JSONObject(object), ok
}

func decodeIdentifier(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	value = trimJavaScriptWhitespace(value)
	return value, value != "" && utf16Length(value) <= validation.EditorGraphIdentifierMaxUTF16
}

func decodeNodeType(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	value = trimJavaScriptWhitespace(value)
	return value, len(value) <= validation.NodeTypeMaxLength && nodeTypePattern.MatchString(value)
}

func decodeErrorPolicy(raw json.RawMessage) (ErrorPolicy, bool) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	policy := ErrorPolicy(value)
	return policy, policy == ErrorPolicyStop || policy == ErrorPolicyErrorBranch
}

func decodeSafePositiveInteger(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	value, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) || value <= 0 || math.Trunc(value) != value || value > float64(maxSafeInteger) {
		return 0, false
	}
	return int64(value), true
}

func decodeFiniteNumber(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	value, err := strconv.ParseFloat(string(raw), 64)
	return value, err == nil && !math.IsInf(value, 0) && !math.IsNaN(value)
}

func unknownFields(object map[string]json.RawMessage, allowed []string, base validation.Path, diagnostics *[]validation.Diagnostic) {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = struct{}{}
	}
	for field := range object {
		if _, ok := allowedSet[field]; !ok {
			// Zod reports one unrecognized_keys issue at the containing object.
			// Retaining that behavior is required by the Backend parity contract.
			*diagnostics = append(*diagnostics, contractDiagnostic(base))
			return
		}
	}
}

func contractDiagnostic(path validation.Path) validation.Diagnostic {
	return validation.NewDiagnostic(validation.CodeEditorGraphContractInvalid, path, "", "")
}

func contractResult(path validation.Path) []validation.Diagnostic {
	return []validation.Diagnostic{contractDiagnostic(path)}
}

func appendPath(base validation.Path, segment any) validation.Path {
	path := append(validation.Path(nil), base...)
	return append(path, segment)
}

func trimJavaScriptWhitespace(value string) string {
	return strings.TrimFunc(value, func(r rune) bool {
		return (unicode.IsSpace(r) && r != '\u0085') || r == '\uFEFF'
	})
}

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }
