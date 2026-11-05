package nodecontract

import (
	"encoding/json"
	"regexp"

	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/editorgraph"
)

const (
	targetModeField = "targetMode"
	absolutePixels  = "ABSOLUTE_PIXELS"
	screenPercent   = "SCREEN_PERCENT"
	selectorID      = "SELECTOR_ID"
	longPress       = "android.long_press"

	maxPixelCoordinate = 100_000
	maxScreenPercent   = 100
	maxSwipeDurationMS = 60_000
	maxSafeInteger     = 9_007_199_254_740_991
)

var targetingInputs = newTargetingInputs()

func newTargetingInputs() map[string]map[string]map[string]compiledField {
	integer := func(minimum, maximum float64) compiledField {
		return compiledField{FieldSchema: FieldSchema{
			Type: FieldTypeInteger, Minimum: &minimum, Maximum: &maximum,
		}}
	}
	pixel := integer(0, maxPixelCoordinate)
	percent := integer(0, maxScreenPercent)
	swipeDuration := integer(1, maxSwipeDurationMS)
	holdDuration := integer(1, maxSafeInteger)

	// Match the Backend selector pattern's ECMAScript whitespace class.
	const space = `\x09-\x0D\x20\x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}`
	selector := compiledField{
		FieldSchema: FieldSchema{Type: FieldTypeString},
		pattern:     regexp.MustCompile(`^[^` + space + `:]+:id/[^/` + space + `]+$`),
	}
	return map[string]map[string]map[string]compiledField{
		"android.tap": {
			absolutePixels: {"x": pixel, "y": pixel},
			screenPercent:  {"xPercent": percent, "yPercent": percent},
			selectorID:     {"selector_id": selector},
		},
		"android.swipe": {
			absolutePixels: {"startX": pixel, "startY": pixel, "endX": pixel, "endY": pixel, "durationMs": swipeDuration},
			screenPercent: {"startXPercent": percent, "startYPercent": percent,
				"endXPercent": percent, "endYPercent": percent, "durationMs": swipeDuration},
		},
		longPress: {
			absolutePixels: {"x": pixel, "y": pixel, "durationMs": holdDuration},
			screenPercent:  {"xPercent": percent, "yPercent": percent, "durationMs": holdDuration},
			selectorID:     {"selector_id": selector, "durationMs": holdDuration},
		},
	}
}

func validateNode(node editorgraph.Node, index int, contract compiledContract) []validation.Diagnostic {
	var diagnostics []validation.Diagnostic
	rawMode, hasMode := node.Config[targetModeField]
	var mode string
	var fields map[string]compiledField
	if hasMode && json.Unmarshal(rawMode, &mode) == nil {
		fields = targetingInputs[node.Type][mode]
	}
	if !hasMode && targetingInputs[node.Type] != nil {
		diagnostics = append(diagnostics, configDiagnostic(node.ID, index, targetModeField))
	}
	for _, field := range contract.required {
		if _, exists := node.Config[field]; !exists || (fields != nil && field == targetModeField) {
			diagnostics = append(diagnostics, configDiagnostic(node.ID, index, field))
		}
	}
	for field, raw := range node.Config {
		if field == targetModeField && fields != nil {
			continue
		}
		schema, exists := contract.properties[field]
		if !exists || !validValue(raw, schema) {
			diagnostics = append(diagnostics, configDiagnostic(node.ID, index, field))
		}
	}
	if fields != nil {
		diagnostics = append(diagnostics, validateTargetBindings(node, index, fields)...)
	}
	return diagnostics
}

func validateTargetBindings(node editorgraph.Node, index int, fields map[string]compiledField) []validation.Diagnostic {
	var diagnostics []validation.Diagnostic
	add := func(path ...any) {
		location := append(validation.Path{"nodes", index, "bindings"}, path...)
		diagnostics = append(diagnostics, validation.NewDiagnostic(validation.CodeNodeBindingsInvalid, location, node.ID, ""))
	}
	for field, schema := range fields {
		var literal editorgraph.JSONObject
		if json.Unmarshal(node.Bindings[field], &literal) != nil || literal == nil {
			add(field)
			continue
		}
		var kind string
		if json.Unmarshal(literal["kind"], &kind) != nil || kind != "literal" {
			add(field, "kind")
		}
		if raw := literal["value"]; raw == nil || string(raw) == "null" || !validValue(raw, schema) {
			add(field, "value")
		}
		for key := range literal {
			if key != "kind" && key != "value" {
				add(field)
				break
			}
		}
	}
	for field := range node.Bindings {
		if _, known := fields[field]; !known {
			add()
			break
		}
	}
	return diagnostics
}
