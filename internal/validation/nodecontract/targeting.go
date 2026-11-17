package nodecontract

import (
	"encoding/json"
	"regexp"
	"strconv"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

const (
	targetModeField = "targetMode"
	absolutePixels  = constants.AndroidTargetPixels
	screenPercent   = constants.AndroidTargetPercent
	selectorID      = constants.AndroidTargetSelector
	longPress       = "android.long_press"

	maxSafeInteger = constants.MaxSafeInteger
)

var targetingInputs = newTargetingInputs()

func newTargetingInputs() map[string]map[string]map[string]compiledField {
	integer := func(minimum, maximum float64) compiledField {
		return compiledField{FieldSchema: FieldSchema{
			Type: FieldTypeInteger, Minimum: &minimum, Maximum: &maximum,
		}}
	}
	pixel := integer(0, constants.MaxAndroidPixelCoordinate)
	screenshotPixel := integer(0, constants.MaxScreenshotDimension)
	percent := integer(0, constants.MaxAndroidScreenPercent)
	swipeDuration := integer(1, constants.MaxAndroidSwipeDurationMS)
	holdDuration := integer(1, maxSafeInteger)

	selector := compiledField{
		FieldSchema: FieldSchema{Type: FieldTypeString},
		pattern:     regexp.MustCompile(constants.AndroidSelectorIDPattern),
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
		constants.ScreenshotNodeType: {
			constants.ScreenshotFullScreen: {},
			absolutePixels:                 {"startX": screenshotPixel, "startY": screenshotPixel, "endX": screenshotPixel, "endY": screenshotPixel},
			screenPercent:                  {"startXPercent": percent, "startYPercent": percent, "endXPercent": percent, "endYPercent": percent},
		},
	}
}

func validateNode(node nodeView, index int, contract compiledContract) []validation.Diagnostic {
	var diagnostics []validation.Diagnostic
	rawMode, hasMode := node.Config[targetModeField]
	var mode string
	var fields map[string]compiledField
	if hasMode && json.Unmarshal(rawMode, &mode) == nil {
		fields = targetingInputs[node.Type][mode]
	}
	if !hasMode && targetingInputs[node.Type] != nil {
		diagnostics = append(diagnostics, node.configDiagnostic(index, targetModeField))
	}
	for _, field := range contract.required {
		if _, exists := node.Config[field]; !exists || (fields != nil && field == targetModeField) {
			diagnostics = append(diagnostics, node.configDiagnostic(index, field))
		}
	}
	for field, raw := range node.Config {
		if field == targetModeField && fields != nil {
			continue
		}
		schema, exists := contract.properties[field]
		if !exists || !validValue(raw, schema) {
			diagnostics = append(diagnostics, node.configDiagnostic(index, field))
		}
	}
	if fields != nil {
		bindingDiagnostics := validateTargetBindings(node, index, fields)
		if len(bindingDiagnostics) == 0 && node.Type == constants.ScreenshotNodeType && mode != constants.ScreenshotFullScreen {
			bindingDiagnostics = validateScreenshotRegion(node, index, mode)
		}
		diagnostics = append(diagnostics, bindingDiagnostics...)
	}
	return diagnostics
}

func validateScreenshotRegion(node nodeView, index int, mode string) []validation.Diagnostic {
	suffix := ""
	if mode == screenPercent {
		suffix = "Percent"
	}
	// The mode binding check has already established valid numeric literals.
	coordinate := func(field string) float64 {
		var binding ir.Object
		_ = json.Unmarshal(node.Inputs[field], &binding)
		value, _ := strconv.ParseFloat(string(binding["value"]), 64)
		return value
	}
	var diagnostics []validation.Diagnostic
	for _, axis := range []string{"X", "Y"} {
		end := "end" + axis + suffix
		if coordinate("start"+axis+suffix) >= coordinate(end) {
			diagnostics = append(diagnostics, validation.NewDiagnostic(validation.CodeNodeBindingsInvalid,
				node.inputPath(index, end, "value"), node.ID, ""))
		}
	}
	return diagnostics
}

func validateTargetBindings(node nodeView, index int, fields map[string]compiledField) []validation.Diagnostic {
	var diagnostics []validation.Diagnostic
	add := func(path ...any) {
		location := node.inputPath(index, path...)
		diagnostics = append(diagnostics, validation.NewDiagnostic(validation.CodeNodeBindingsInvalid, location, node.ID, ""))
	}
	for field, schema := range fields {
		var literal ir.Object
		if json.Unmarshal(node.Inputs[field], &literal) != nil || literal == nil {
			add(field)
			continue
		}
		var kind string
		if json.Unmarshal(literal[node.form.source], &kind) != nil || kind != bindingLiteral {
			add(field, node.form.source)
		}
		if raw := literal["value"]; raw == nil || string(raw) == "null" || !validValue(raw, schema) {
			add(field, "value")
		}
		for key := range literal {
			if key != node.form.source && key != "value" {
				add(field)
				break
			}
		}
	}
	for field := range node.Inputs {
		if _, known := fields[field]; !known {
			add()
			break
		}
	}
	return diagnostics
}
