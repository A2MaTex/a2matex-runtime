package adb

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"io"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// SelectorPoint resolves one resource ID on the current default display.
func (d *Dispatcher) SelectorPoint(ctx context.Context, serial, resourceID string) (TapCommand, error) {
	size, err := d.DefaultDisplaySize(ctx, serial)
	if err != nil {
		return TapCommand{}, err
	}
	data, err := d.readHierarchy(ctx, serial)
	if err != nil {
		return TapCommand{}, err
	}
	if err := ctx.Err(); err != nil {
		return TapCommand{}, xerror.WithOperation("resolve_selector", err)
	}
	point, err := findSelectorPoint(ctx, data, resourceID, size)
	return point, xerror.WithOperation("resolve_selector", err)
}

func (d *Dispatcher) readHierarchy(ctx context.Context, serial string) ([]byte, error) {
	directory := "/data/local/tmp/a2matex-" + rand.Text()
	filename := directory + "/window.xml"
	limit := constants.ProcessOutputLimitBytes
	if _, err := d.runDevice(ctx, serial, "create_hierarchy_directory", []string{"shell", "-T", "mkdir", "-m", "700", directory}, limit, 0); err != nil {
		return nil, err
	}
	defer d.removeHierarchy(ctx, serial, directory, filename)
	if _, err := d.runDevice(ctx, serial, "dump_hierarchy", []string{"shell", "-T", "uiautomator", "dump", filename}, limit, 0); err != nil {
		return nil, err
	}
	// exec-out keeps XML bytes intact. Only the generated path reaches the shell.
	result, err := d.runDevice(ctx, serial, "read_hierarchy", []string{"exec-out", "cat", filename}, constants.AndroidHierarchyMaxBytes, 0)
	if err != nil {
		return nil, err
	}
	if result.StdoutTruncated {
		return nil, xerror.WithOperation("read_hierarchy", hierarchyError("Android UI hierarchy exceeds the size limit"))
	}
	return result.Stdout, nil
}

func (d *Dispatcher) removeHierarchy(ctx context.Context, serial, directory, filename string) {
	if ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, constants.AndroidHierarchyCleanup)
	defer cancel()
	limit := constants.ProcessOutputLimitBytes
	_, _ = d.runDevice(ctx, serial, "remove_hierarchy_file", []string{"shell", "-T", "rm", "-f", filename}, limit, 0)
	_, _ = d.runDevice(ctx, serial, "remove_hierarchy_directory", []string{"shell", "-T", "rmdir", directory}, limit, 0)
}

func hierarchyError(message string) error {
	return xerror.CommandError{Code: constants.CommandFailed, Message: message}
}

func findSelectorPoint(ctx context.Context, data []byte, resourceID string, size DisplaySize) (TapCommand, error) {
	var point TapCommand
	if len(data) > constants.AndroidHierarchyMaxBytes || !utf8.Valid(data) {
		return point, hierarchyError("Android UI hierarchy is invalid")
	}
	parser := xml.NewDecoder(bytes.NewReader(data))
	var visible []bool
	var bounds string
	seenDeclaration, seenRoot, matched, matchVisible := false, false, false, false
	for {
		if err := ctx.Err(); err != nil {
			return point, err
		}
		token, err := parser.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return point, hierarchyError("Android UI hierarchy is invalid")
		}
		switch token := token.(type) {
		case xml.StartElement:
			if len(visible) == 0 {
				if seenRoot || token.Name != (xml.Name{Local: "hierarchy"}) {
					return point, hierarchyError("Android UI hierarchy is invalid")
				}
				seenRoot = true
				visible = append(visible, true)
				continue
			}
			if token.Name != (xml.Name{Local: "node"}) {
				return point, hierarchyError("Android UI hierarchy is invalid")
			}
			id, nodeBounds, nodeVisible, err := hierarchyNode(token.Attr)
			if err != nil {
				return point, err
			}
			nodeVisible = nodeVisible && visible[len(visible)-1]
			visible = append(visible, nodeVisible)
			if id == resourceID {
				if matched {
					return point, hierarchyError("Android selector matches more than one node")
				}
				matched, matchVisible, bounds = true, nodeVisible, nodeBounds
			}
		case xml.EndElement:
			visible = visible[:len(visible)-1]
		case xml.CharData:
			if len(bytes.TrimSpace(token)) > 0 {
				return point, hierarchyError("Android UI hierarchy is invalid")
			}
		case xml.Directive:
			return point, hierarchyError("Android UI hierarchy is invalid")
		case xml.ProcInst:
			if seenRoot || seenDeclaration || token.Target != "xml" {
				return point, hierarchyError("Android UI hierarchy is invalid")
			}
			seenDeclaration = true
		}
	}
	if !seenRoot {
		return point, hierarchyError("Android UI hierarchy is invalid")
	}
	if !matched || !matchVisible {
		return point, hierarchyError("Android selector has no visible match")
	}
	return clippedCenter(bounds, size)
}

func hierarchyNode(attributes []xml.Attr) (id, bounds string, visible bool, err error) {
	var hasID, hasBounds, hasVisibility bool
	visible = true
	for _, attribute := range attributes {
		if attribute.Name.Space != "" {
			continue
		}
		switch attribute.Name.Local {
		case "resource-id":
			if hasID {
				return "", "", false, hierarchyError("Android UI hierarchy is invalid")
			}
			id, hasID = attribute.Value, true
		case "bounds":
			if hasBounds {
				return "", "", false, hierarchyError("Android UI hierarchy is invalid")
			}
			bounds, hasBounds = attribute.Value, true
		case "visible-to-user":
			if hasVisibility || (attribute.Value != "true" && attribute.Value != "false") {
				return "", "", false, hierarchyError("Android UI hierarchy is invalid")
			}
			visible, hasVisibility = attribute.Value == "true", true
		}
	}
	return id, bounds, visible, nil
}

var nodeBounds = regexp.MustCompile(`^\[(-?[0-9]+),(-?[0-9]+)\]\[(-?[0-9]+),(-?[0-9]+)\]$`)

func clippedCenter(bounds string, size DisplaySize) (TapCommand, error) {
	match := nodeBounds.FindStringSubmatch(bounds)
	if match != nil {
		var coordinates [4]int
		for index, value := range match[1:] {
			number, err := strconv.ParseInt(value, 10, 32)
			if err != nil {
				return TapCommand{}, hierarchyError("Android selector has invalid bounds")
			}
			coordinates[index] = int(number)
		}
		left, top := max(0, coordinates[0]), max(0, coordinates[1])
		right, bottom := min(size.Width, coordinates[2]), min(size.Height, coordinates[3])
		if left < right && top < bottom {
			return TapCommand{X: left + (right-left)/2, Y: top + (bottom-top)/2}, nil
		}
	}
	return TapCommand{}, hierarchyError("Android selector has invalid bounds")
}
