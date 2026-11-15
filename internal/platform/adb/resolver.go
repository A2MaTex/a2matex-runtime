// Package adb selects the local Android Debug Bridge toolchain.
package adb

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/config"
	"github.com/A2MaTex/a2matex-runtime/internal/platform/processrunner"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type Source string

const (
	Configured      Source = "configured"
	Bundled         Source = "bundled"
	DevelopmentPath Source = "development_path"
)

type Toolchain struct {
	Executable string
	Source     Source
	Version    string
}

type resolution struct {
	done      chan struct{}
	toolchain Toolchain
	err       error
}

// Resolver shares one in-flight resolution and retains one verified toolchain.
type Resolver struct {
	cfg        config.ADBConfig
	executable func() (string, error)
	path       func() string
	run        func(context.Context, string, []string, time.Duration) processrunner.Result

	mu       sync.Mutex
	verified Toolchain
	pending  *resolution
}

func NewResolver(cfg config.ADBConfig) *Resolver {
	return &Resolver{
		cfg:        cfg,
		executable: os.Executable,
		path:       func() string { return os.Getenv("PATH") },
		run:        processrunner.Run,
	}
}

func (r *Resolver) Resolve(ctx context.Context) (Toolchain, error) {
	if err := ctx.Err(); err != nil {
		return Toolchain{}, err
	}
	r.mu.Lock()
	if r.verified.Executable != "" {
		verified := r.verified
		r.mu.Unlock()
		return verified, nil
	}
	if pending := r.pending; pending != nil {
		r.mu.Unlock()
		select {
		case <-pending.done:
			if err := ctx.Err(); err != nil {
				return Toolchain{}, err
			}
			return pending.toolchain, pending.err
		case <-ctx.Done():
			return Toolchain{}, ctx.Err()
		}
	}
	pending := &resolution{done: make(chan struct{})}
	r.pending = pending
	r.mu.Unlock()

	toolchain, err := r.resolve(ctx)

	r.mu.Lock()
	if contextErr := ctx.Err(); contextErr != nil {
		toolchain, err = Toolchain{}, contextErr
	}
	if err == nil {
		r.verified = toolchain
	}
	pending.toolchain, pending.err = toolchain, err
	r.pending = nil
	close(pending.done)
	r.mu.Unlock()
	return toolchain, err
}

var versionLine = regexp.MustCompile(`(?m)^Android Debug Bridge version[ \t]+([0-9]+(?:\.[0-9]+)+)[ \t]*\r?$`)

func (r *Resolver) resolve(ctx context.Context) (Toolchain, error) {
	candidate, err := r.selectCandidate()
	if err != nil {
		return Toolchain{}, err
	}
	if err := ctx.Err(); err != nil {
		return Toolchain{}, err
	}
	result := r.run(ctx, candidate.Executable, []string{"version"}, constants.ADBVersionProbeTimeout)
	if err := ctx.Err(); err != nil {
		return Toolchain{}, err
	}
	if result.Outcome != processrunner.Succeeded || result.ExitCode == nil || *result.ExitCode != 0 {
		return Toolchain{}, fmt.Errorf("%w (%s, %s)", xerror.ErrADBVersionProbe, candidate.Source, result.Outcome)
	}
	if result.StdoutTruncated {
		return Toolchain{}, fmt.Errorf("%w (%s, truncated output)", xerror.ErrADBVersionProbe, candidate.Source)
	}
	match := versionLine.FindSubmatch(bytes.TrimPrefix(result.Stdout, []byte{0xef, 0xbb, 0xbf}))
	if match == nil {
		return Toolchain{}, fmt.Errorf("%w (%s, invalid version output)", xerror.ErrADBVersionProbe, candidate.Source)
	}
	candidate.Version = string(match[1])
	return candidate, nil
}
