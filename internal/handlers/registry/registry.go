package registry

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
)

type Invocation struct {
	ADBSerial string
	Config    map[string]json.RawMessage
	Inputs    map[string]json.RawMessage
	// StageScreenshot is supplied by the runner for the active Screenshot attempt.
	StageScreenshot func(context.Context, []byte) (string, error)
	// ResolveInput and SelectPort are synchronous, attempt-scoped Switch capabilities.
	// Handlers must not retain these callbacks or call them from another goroutine.
	ResolveInput func(context.Context, string) (json.RawMessage, error)
	SelectPort   func(context.Context, string) error
}

type Handler func(context.Context, Invocation) (map[string]any, error)

type Registration struct {
	Type           string
	RuntimeHandler string
	Run            Handler
}

type SupportedNode struct {
	Type           string
	RuntimeHandler string
}

// Registry holds the immutable set of handlers enabled by this build.
type Registry struct {
	byHandler map[string]Registration
	supported []SupportedNode
}

func New(catalog *nodecontract.Registry, entries []Registration) (*Registry, error) {
	if catalog == nil {
		return nil, errors.New("execution registry requires the installed node catalog")
	}
	byHandler := make(map[string]Registration, len(entries))
	supported := make([]SupportedNode, 0, len(entries))
	seenTypes := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.Type == "" || entry.RuntimeHandler == "" || entry.Run == nil {
			return nil, fmt.Errorf("incomplete handler registration for type %q and key %q", entry.Type, entry.RuntimeHandler)
		}
		definition, ok := catalog.CompilationDefinition(entry.Type)
		if !ok {
			return nil, fmt.Errorf("unknown registered type %q", entry.Type)
		}
		if definition.RuntimeHandler != entry.RuntimeHandler {
			return nil, fmt.Errorf("handler key %q does not match the installed definition for %q", entry.RuntimeHandler, entry.Type)
		}
		if _, exists := seenTypes[entry.Type]; exists {
			return nil, fmt.Errorf("duplicate registered type %q", entry.Type)
		}
		if _, exists := byHandler[entry.RuntimeHandler]; exists {
			return nil, fmt.Errorf("duplicate handler key %q", entry.RuntimeHandler)
		}
		seenTypes[entry.Type] = struct{}{}
		byHandler[entry.RuntimeHandler] = entry
		supported = append(supported, SupportedNode{Type: entry.Type, RuntimeHandler: entry.RuntimeHandler})
	}
	slices.SortFunc(supported, func(a, b SupportedNode) int {
		return cmp.Compare(a.Type, b.Type)
	})
	return &Registry{byHandler: byHandler, supported: supported}, nil
}

func (r *Registry) Lookup(nodeType, handlerKey string) (Handler, bool) {
	entry, ok := r.byHandler[handlerKey]
	if !ok || entry.Type != nodeType {
		return nil, false
	}
	return entry.Run, true
}

func (r *Registry) SupportedNodes() []SupportedNode {
	return slices.Clone(r.supported)
}
