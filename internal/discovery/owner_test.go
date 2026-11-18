package discovery

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestOwnerWatchPreservesStopCause(t *testing.T) {
	for _, tc := range []struct {
		frame string
		cause error
	}{
		{"shutdown\n", xerror.ErrOwnerShutdownRequested}, {"", xerror.ErrOwnerDisconnected},
		{"shutdown", xerror.ErrOwnerProtocol}, {"secret-invalid-frame\n", xerror.ErrOwnerProtocol},
	} {
		t.Run(tc.cause.Error()+tc.frame, func(t *testing.T) {
			commands, parent, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer commands.Close()
			defer parent.Close()
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			done := make(chan struct{})
			go func() { (&Owner{commands: commands}).Watch(cancel); close(done) }()
			if _, err := parent.WriteString(tc.frame); err != nil {
				t.Fatal(err)
			}
			_ = parent.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("owner watcher did not return")
			}
			if !errors.Is(context.Cause(ctx), tc.cause) {
				t.Fatalf("cause: %v", context.Cause(ctx))
			}
		})
	}
}
