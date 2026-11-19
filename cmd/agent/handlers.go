package main

import (
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/delay"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/end"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/inputtext"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/keyevent"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/launchapp"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/longpress"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/start"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/swipe"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/switcher"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/tap"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/adb"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
)

func newHandlerRegistry(catalog *nodecontract.Registry, dispatcher *adb.Dispatcher) (*registry.Registry, error) {
	keyEvents := keyevent.NewKeyEvents(dispatcher)
	return registry.New(catalog, []registry.Registration{
		{Type: "flow.start", RuntimeHandler: "flow.start", Run: start.Invoke},
		{Type: "flow.switch", RuntimeHandler: "flow.switch", Run: switcher.Invoke},
		{Type: "flow.end", RuntimeHandler: "flow.end", Run: end.Invoke},
		{Type: "flow.delay", RuntimeHandler: "flow.delay", Run: delay.Invoke},
		{Type: "android.tap", RuntimeHandler: "android.tap", Run: tap.NewTap(dispatcher).Invoke},
		{Type: "android.swipe", RuntimeHandler: "android.swipe", Run: swipe.NewSwipe(dispatcher).Invoke},
		{Type: "android.long_press", RuntimeHandler: "android.long_press", Run: longpress.NewLongPress(dispatcher).Invoke},
		{Type: "android.press_back", RuntimeHandler: "android.press_back", Run: keyEvents.PressBack},
		{Type: "android.press_home", RuntimeHandler: "android.press_home", Run: keyEvents.PressHome},
		{Type: "android.launch_app", RuntimeHandler: "android.launch_app", Run: launchapp.NewLaunchApp(dispatcher).Invoke},
		{Type: "android.input_text", RuntimeHandler: "android.input_text", Run: inputtext.NewInputText(dispatcher).Invoke},
	})
}
