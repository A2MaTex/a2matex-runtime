package runtimeir

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

func (c *checks) decode(raw json.RawMessage) *ir.Document {
	root := validation.Path{"runtimeIr"}
	if !utf8.Valid(raw) {
		c.contract(root)
		return nil
	}
	object := c.object(raw, root)
	if object == nil {
		return nil
	}
	version := c.integer(object["formatVersion"], appendPath(root, "formatVersion"), 1, constants.MaxSafeInteger)
	if c.structural {
		return nil
	}
	if version != constants.RuntimeIRFormatVersion {
		c.add(validation.NewDiagnostic(validation.CodeRuntimeIRFormatUnsupported, appendPath(root, "formatVersion"), "", ""))
		return nil
	}
	c.keys(object, root, "formatVersion", "minimumRuntimeVersion", "metadata", "entryNodeId", "inputSchema", "nodes", "edges", "requiredCapabilities")
	document := &ir.Document{FormatVersion: int(version)}
	document.MinimumRuntimeVersion = c.string(object["minimumRuntimeVersion"], appendPath(root, "minimumRuntimeVersion"), ir.ValidVersion)
	document.EntryNodeID = c.string(object["entryNodeId"], appendPath(root, "entryNodeId"), ir.ValidIdentifier)
	metadataPath := appendPath(root, "metadata")
	metadata := c.object(object["metadata"], metadataPath)
	if metadata != nil {
		c.keys(metadata, metadataPath, "sourceGraphSchemaVersion")
		document.Metadata.SourceGraphSchemaVersion = c.integer(metadata["sourceGraphSchemaVersion"], appendPath(metadataPath, "sourceGraphSchemaVersion"), 1, constants.MaxSafeInteger)
		if document.Metadata.SourceGraphSchemaVersion > 0 && document.Metadata.SourceGraphSchemaVersion != validation.EditorGraphSchemaVersion {
			c.add(validation.NewDiagnostic(validation.CodeGraphSchemaVersionUnsupported, appendPath(metadataPath, "sourceGraphSchemaVersion"), "", ""))
		}
	}
	if c.object(object["inputSchema"], appendPath(root, "inputSchema")) != nil {
		document.InputSchema = bytes.Clone(object["inputSchema"])
	}
	nodes := c.array(object["nodes"], appendPath(root, "nodes"))
	edges := c.array(object["edges"], appendPath(root, "edges"))
	if nodes != nil && len(nodes) < 2 {
		c.contract(appendPath(root, "nodes"))
	}
	if edges != nil && len(edges) < 1 {
		c.contract(appendPath(root, "edges"))
	}
	if len(nodes) > constants.RuntimeIRMaxNodes {
		c.add(validation.NewDiagnostic(validation.CodeGraphNodeLimitExceeded, appendPath(root, "nodes"), "", ""))
	}
	if len(edges) > constants.RuntimeIRMaxEdges {
		c.add(validation.NewDiagnostic(validation.CodeGraphEdgeLimitExceeded, appendPath(root, "edges"), "", ""))
	}
	if len(nodes) > constants.RuntimeIRMaxNodes || len(edges) > constants.RuntimeIRMaxEdges {
		return nil
	}
	document.Nodes = make([]ir.Node, 0, len(nodes))
	for index, rawNode := range nodes {
		if c.ctx.Err() != nil {
			return nil
		}
		document.Nodes = append(document.Nodes, c.node(rawNode, index))
	}
	document.Edges = make([]ir.Edge, 0, len(edges))
	for index, rawEdge := range edges {
		if c.ctx.Err() != nil {
			return nil
		}
		path := appendPath(root, "edges", index)
		edge := c.object(rawEdge, path)
		if edge == nil {
			continue
		}
		c.keys(edge, path, "id", "sourceNodeId", "sourcePort", "targetNodeId")
		document.Edges = append(document.Edges, ir.Edge{
			ID:           c.string(edge["id"], appendPath(path, "id"), ir.ValidIdentifier),
			SourceNodeID: c.string(edge["sourceNodeId"], appendPath(path, "sourceNodeId"), ir.ValidIdentifier),
			SourcePort:   c.string(edge["sourcePort"], appendPath(path, "sourcePort"), ir.ValidIdentifier),
			TargetNodeID: c.string(edge["targetNodeId"], appendPath(path, "targetNodeId"), ir.ValidIdentifier),
		})
	}
	capabilitiesPath := appendPath(root, "requiredCapabilities")
	capabilities := c.array(object["requiredCapabilities"], capabilitiesPath)
	document.RequiredCapabilities = make([]string, 0, len(capabilities))
	for index, rawCapability := range capabilities {
		document.RequiredCapabilities = append(document.RequiredCapabilities, c.string(rawCapability, appendPath(capabilitiesPath, index), ir.ValidCode))
	}
	if c.structural {
		return nil
	}
	return document
}

func (c *checks) node(raw json.RawMessage, index int) ir.Node {
	path := validation.Path{"runtimeIr", "nodes", index}
	object := c.object(raw, path)
	if object == nil {
		return ir.Node{}
	}
	c.keys(object, path, "id", "type", "runtimeHandler", "config", "inputs", "timeoutMs", "retryPolicy", "errorPolicy")
	node := ir.Node{
		ID:             c.string(object["id"], appendPath(path, "id"), ir.ValidIdentifier),
		Type:           c.string(object["type"], appendPath(path, "type"), ir.ValidCode),
		RuntimeHandler: c.string(object["runtimeHandler"], appendPath(path, "runtimeHandler"), ir.ValidCode),
		Config:         c.object(object["config"], appendPath(path, "config")),
		Inputs:         c.object(object["inputs"], appendPath(path, "inputs")),
		TimeoutMS:      c.integer(object["timeoutMs"], appendPath(path, "timeoutMs"), 1, validation.NodeTimeoutMaxMS),
		ErrorPolicy: c.string(object["errorPolicy"], appendPath(path, "errorPolicy"), func(value string) bool {
			return value == constants.ErrorPolicyStop || value == constants.ErrorPolicyErrorBranch
		}),
	}
	if len(node.Config) > constants.RuntimeIRMaxFields {
		c.contract(appendPath(path, "config"))
	}
	if len(node.Inputs) > constants.RuntimeIRMaxFields {
		c.contract(appendPath(path, "inputs"))
	} else {
		for name, source := range node.Inputs {
			c.source(source, appendPath(path, "inputs", name))
		}
	}
	retryPath := appendPath(path, "retryPolicy")
	retry := c.object(object["retryPolicy"], retryPath)
	if retry != nil {
		c.keys(retry, retryPath, "maxAttempts", "delayMs")
		node.RetryPolicy.MaxAttempts = c.integer(retry["maxAttempts"], appendPath(retryPath, "maxAttempts"), 1, constants.MaxNodeAttemptNumber)
		node.RetryPolicy.DelayMS = c.integer(retry["delayMs"], appendPath(retryPath, "delayMs"), 0, constants.MaxRetryDelayMS)
	}
	return node
}

func (c *checks) source(raw json.RawMessage, path validation.Path) {
	object := c.object(raw, path)
	if object == nil {
		return
	}
	var tag string
	if json.Unmarshal(object["source"], &tag) != nil {
		c.contract(path)
		return
	}
	switch tag {
	case constants.InputSourceLiteral:
		c.keys(object, path, "source", "value")
		if _, ok := nodecontract.ScalarType(object["value"]); !ok {
			c.contract(appendPath(path, "value"))
		}
	case constants.InputSourceWorkflowInput:
		c.keys(object, path, "source", "inputName")
		c.string(object["inputName"], appendPath(path, "inputName"), ir.ValidFieldName)
	case constants.InputSourceNodeOutput:
		c.keys(object, path, "source", "nodeId", "field")
		c.string(object["nodeId"], appendPath(path, "nodeId"), ir.ValidIdentifier)
		c.string(object["field"], appendPath(path, "field"), ir.ValidFieldName)
	case constants.InputSourceFlowVariable, constants.InputSourceGlobalVariable:
		c.keys(object, path, "source", "name")
		c.string(object["name"], appendPath(path, "name"), ir.ValidVariableName)
	default:
		c.contract(path)
	}
}

func (c *checks) object(raw json.RawMessage, path validation.Path) ir.Object {
	var object ir.Object
	if json.Unmarshal(raw, &object) != nil || object == nil {
		c.contract(path)
		return nil
	}
	return object
}

func (c *checks) array(raw json.RawMessage, path validation.Path) []json.RawMessage {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		c.contract(path)
		return nil
	}
	return values
}

func (c *checks) keys(object ir.Object, path validation.Path, keys ...string) {
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	for key := range object {
		if !allowed[key] {
			c.contract(path)
			break
		}
	}
}

func (c *checks) string(raw json.RawMessage, path validation.Path, valid func(string) bool) string {
	var value *string
	if json.Unmarshal(raw, &value) != nil || value == nil || !valid(*value) {
		c.contract(path)
		return ""
	}
	return *value
}

func (c *checks) integer(raw json.RawMessage, path validation.Path, minimum, maximum int64) int64 {
	value, err := strconv.ParseFloat(string(bytes.TrimSpace(raw)), 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) || math.Trunc(value) != value || value < float64(minimum) || value > float64(maximum) {
		c.contract(path)
		return 0
	}
	return int64(value)
}

func appendPath(path validation.Path, parts ...any) validation.Path {
	return append(append(validation.Path{}, path...), parts...)
}
