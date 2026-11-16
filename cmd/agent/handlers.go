package main

import (
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/delay"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/end"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/start"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
)

func newHandlerRegistry(catalog *nodecontract.Registry) (*registry.Registry, error) {
	return registry.New(catalog, []registry.Registration{
		{Type: "flow.start", RuntimeHandler: "flow.start", Run: start.Invoke},
		{Type: "flow.end", RuntimeHandler: "flow.end", Run: end.Invoke},
		{Type: "flow.delay", RuntimeHandler: "flow.delay", Run: delay.Invoke},
	})
}
