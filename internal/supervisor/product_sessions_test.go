package supervisor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/discovery"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productsession"
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type sessionPackages struct{}

func (sessionPackages) Verify(context.Context, packagevalidation.Request) (*packagevalidation.VerifiedPackage, error) {
	return nil, errors.New("lifecycle must not read a package")
}

func TestProductSessionComponentOrderAndStartupRollback(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			sessions, err := productsession.New(sessionPackages{}, nil, logger)
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{Production: config.ProductionConfig{AuthorizationEnabled: enabled}, ShutdownTimeout: time.Second}
			app := New(nil, discovery.NewPublisher(cfg, nil), nil, nil, nil, nil, nil, nil, sessions, cfg, logger)
			var names []string
			failure := errors.New("controlled HTTP startup failure")
			for i, component := range app.components {
				names = append(names, component.name)
				if component.name == "product_sessions" {
					continue
				}
				var startErr error
				if component.name == "local_http_server" {
					startErr = failure
				}
				app.components[i].lifecycle = newFakeLifecycle(component.name, startErr)
			}
			want := []string{"execution_store", "execution_recovery", "artifact_store", "event_retention", "execution_checkpoint", "execution_lifecycle"}
			if enabled {
				want = append(want, "product_sessions")
			}
			want = append(want, "local_http_server", "connection_file_publisher")
			if !reflect.DeepEqual(names, want) {
				t.Fatal("component order", names)
			}
			if err := app.Run(t.Context()); !errors.Is(err, failure) {
				t.Fatal("startup failure lost", err)
			}
			if enabled {
				if err := sessions.Wait(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := sessions.Prepare(t.Context(), productsession.Request{}); !errors.Is(err, xerror.ErrProductSessionUnavailable) {
				t.Fatal("rollback/disabled session remained available", err)
			}
		})
	}
}
