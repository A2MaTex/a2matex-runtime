package httpserver

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
)

func TestStartReturnsBindFailureSynchronously(t *testing.T) {
	t.Parallel()

	server := New(
		config.Config{HTTP: config.HTTPConfig{ListenAddress: "127.0.0.1"}},
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	)

	err := server.Start(context.Background())
	if err == nil {
		t.Fatal("Start() error = nil, want bind failure")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want bind failure", err)
	}
}
