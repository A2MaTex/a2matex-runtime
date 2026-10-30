package validation

import (
	"encoding/json"
	"sort"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

type Status string

const (
	StatusInvalid           Status = "INVALID"
	StatusValidWithWarnings Status = "VALID_WITH_WARNINGS"
	StatusValid             Status = "VALID"
)

type Code string

const (
	CodeEditorGraphContractInvalid    Code = "EDITOR_GRAPH_CONTRACT_INVALID"
	CodeGraphSchemaVersionUnsupported Code = "GRAPH_SCHEMA_VERSION_UNSUPPORTED"
	CodeNodeIDDuplicated              Code = "NODE_ID_DUPLICATED"
	CodeEdgeIDDuplicated              Code = "EDGE_ID_DUPLICATED"
	CodeStartNodeCountInvalid         Code = "START_NODE_COUNT_INVALID"
	CodeEntryNodeInvalid              Code = "ENTRY_NODE_INVALID"
	CodeEndNodeUnreachable            Code = "END_NODE_UNREACHABLE"
	CodeEdgeSourceNodeNotFound        Code = "EDGE_SOURCE_NODE_NOT_FOUND"
	CodeEdgeTargetNodeNotFound        Code = "EDGE_TARGET_NODE_NOT_FOUND"
	CodeEdgeSourcePortNotFound        Code = "EDGE_SOURCE_PORT_NOT_FOUND"
	CodeEndNodeHasOutgoingEdge        Code = "END_NODE_HAS_OUTGOING_EDGE"
	CodeNodeUnreachable               Code = "NODE_UNREACHABLE"
	CodeGraphCycleDetected            Code = "GRAPH_CYCLE_DETECTED"
	CodeNodeTypeNotActive             Code = "NODE_TYPE_NOT_ACTIVE"
	CodeNodeConfigInvalid             Code = "NODE_CONFIG_INVALID"
	CodeGraphNodeLimitExceeded        Code = "GRAPH_NODE_LIMIT_EXCEEDED"
	CodeGraphEdgeLimitExceeded        Code = "GRAPH_EDGE_LIMIT_EXCEEDED"
)

const (
	EditorGraphSchemaVersion      = 1
	EditorGraphMaxContentBytes    = constants.EditorGraphMaxContentBytes
	EditorGraphMaxNodes           = 500
	EditorGraphMaxEdges           = 1_000
	EditorGraphMaxDiagnostics     = 100
	EditorGraphIdentifierMaxUTF16 = 100
	NodeTypeMaxLength             = 255
)

var messages = map[Code]string{
	CodeEditorGraphContractInvalid:    "Editor Graph does not match the required contract",
	CodeGraphSchemaVersionUnsupported: "Editor Graph schema version is not supported",
	CodeNodeIDDuplicated:              "Node ID must be unique",
	CodeEdgeIDDuplicated:              "Edge ID must be unique",
	CodeStartNodeCountInvalid:         "Graph must contain exactly one Start node",
	CodeEntryNodeInvalid:              "Entry node must reference the single Start node",
	CodeEndNodeUnreachable:            "Graph must contain at least one End node reachable from Start",
	CodeEdgeSourceNodeNotFound:        "Edge source node does not exist",
	CodeEdgeTargetNodeNotFound:        "Edge target node does not exist",
	CodeEdgeSourcePortNotFound:        "Edge source port does not exist on the source node definition",
	CodeEndNodeHasOutgoingEdge:        "End node cannot have outgoing edges",
	CodeNodeUnreachable:               "Node is not reachable from Start",
	CodeGraphCycleDetected:            "Graph contains an uncontrolled cycle",
	CodeNodeTypeNotActive:             "Node type does not exist in the active Node Catalog",
	CodeNodeConfigInvalid:             "Node configuration does not match its definition",
	CodeGraphNodeLimitExceeded:        "Graph exceeds the maximum node count",
	CodeGraphEdgeLimitExceeded:        "Graph exceeds the maximum edge count",
}

type Path []any

type Diagnostic struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	NodeID  string `json:"nodeId,omitempty"`
	EdgeID  string `json:"edgeId,omitempty"`
	Path    Path   `json:"path"`
}

type Result struct {
	Status    Status       `json:"status"`
	Errors    []Diagnostic `json:"errors"`
	Warnings  []Diagnostic `json:"warnings"`
	Truncated bool         `json:"truncated"`
}

func NewDiagnostic(code Code, path Path, nodeID, edgeID string) Diagnostic {
	return Diagnostic{Code: code, Message: messages[code], NodeID: nodeID, EdgeID: edgeID, Path: path}
}

func Message(code Code) string { return messages[code] }

func BuildResult(errors, warnings []Diagnostic) Result {
	errors = append([]Diagnostic(nil), errors...)
	warnings = append([]Diagnostic(nil), warnings...)
	sort.SliceStable(errors, func(i, j int) bool { return compareDiagnostic(errors[i], errors[j]) < 0 })
	sort.SliceStable(warnings, func(i, j int) bool { return compareDiagnostic(warnings[i], warnings[j]) < 0 })

	total := len(errors) + len(warnings)
	if len(errors) > EditorGraphMaxDiagnostics {
		errors = errors[:EditorGraphMaxDiagnostics]
	}
	remaining := EditorGraphMaxDiagnostics - len(errors)
	if len(warnings) > remaining {
		warnings = warnings[:remaining]
	}
	if errors == nil {
		errors = []Diagnostic{}
	}
	if warnings == nil {
		warnings = []Diagnostic{}
	}

	status := StatusValid
	if len(errors) > 0 {
		status = StatusInvalid
	} else if len(warnings) > 0 {
		status = StatusValidWithWarnings
	}

	return Result{Status: status, Errors: errors, Warnings: warnings, Truncated: total > EditorGraphMaxDiagnostics}
}

func compareDiagnostic(left, right Diagnostic) int {
	if result := compareString(string(left.Code), string(right.Code)); result != 0 {
		return result
	}
	if result := compareString(left.NodeID, right.NodeID); result != 0 {
		return result
	}
	if result := compareString(left.EdgeID, right.EdgeID); result != 0 {
		return result
	}
	return comparePath(left.Path, right.Path)
}

func comparePath(left, right Path) int {
	limit := min(len(left), len(right))
	for i := 0; i < limit; i++ {
		leftInt, leftIsInt := left[i].(int)
		rightInt, rightIsInt := right[i].(int)
		switch {
		case leftIsInt && rightIsInt:
			if leftInt < rightInt {
				return -1
			}
			if leftInt > rightInt {
				return 1
			}
		case leftIsInt:
			return -1
		case rightIsInt:
			return 1
		default:
			leftString, _ := left[i].(string)
			rightString, _ := right[i].(string)
			if result := compareString(leftString, rightString); result != 0 {
				return result
			}
		}
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return 0
}

func compareString(left, right string) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func (p Path) MarshalJSON() ([]byte, error) {
	if p == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]any(p))
}
