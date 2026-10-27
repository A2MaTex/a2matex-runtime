package supervisor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunRollsBackStartedComponentsWhenLaterStartFails(t *testing.T) {
	t.Parallel()

	startFailure := errors.New("publication failed")
	server := newFakeLifecycle("server", nil)
	publisher := newFakeLifecycle("publisher", startFailure)
	application := testApp(server, publisher)

	err := application.Run(context.Background())
	if !errors.Is(err, startFailure) {
		t.Fatalf("Run() error = %v, want start failure", err)
	}
	if got := server.shutdownCount(); got != 1 {
		t.Fatalf("server shutdown count = %d, want 1", got)
	}
	if got := publisher.shutdownCount(); got != 0 {
		t.Fatalf("publisher shutdown count = %d, want 0", got)
	}
}

func TestRunStopsStartupWhenStartedComponentTerminates(t *testing.T) {
	t.Parallel()

	waitFailure := errors.New("server failed")
	cleanupFailure := errors.New("server cleanup failed")
	server := newFakeLifecycle("server", nil)
	server.shutdownErr = cleanupFailure
	publisher := newFakeLifecycle("publisher", nil)
	publisher.blockStart = true

	var logs bytes.Buffer
	application := testApp(server, publisher)
	application.logger = slog.New(slog.NewJSONHandler(&logs, nil))

	runResult := make(chan error, 1)
	go func() {
		runResult <- application.Run(context.Background())
	}()

	<-publisher.started
	server.complete(waitFailure)

	err := receiveRunResult(t, runResult)
	if !errors.Is(err, waitFailure) {
		t.Fatalf("Run() error = %v, want component failure", err)
	}
	if !errors.Is(err, cleanupFailure) {
		t.Fatalf("Run() error = %v, want cleanup failure", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, cancellation must not replace the component failure", err)
	}
	if strings.Contains(logs.String(), `"event":"runtime_ready"`) {
		t.Fatalf("runtime reported readiness after component failure: %s", logs.String())
	}
	if got := publisher.shutdownCount(); got != 0 {
		t.Fatalf("publisher shutdown count = %d, want 0 for incomplete start", got)
	}
	assertClosed(t, server.waitReturned, "server Wait")
}

func TestRunTreatsEarlyNilWaitAsUnexpectedStop(t *testing.T) {
	t.Parallel()

	server := newFakeLifecycle("server", nil)
	publisher := newFakeLifecycle("publisher", nil)
	publisher.blockStart = true
	application := testApp(server, publisher)

	runResult := make(chan error, 1)
	go func() {
		runResult <- application.Run(context.Background())
	}()

	<-publisher.started
	server.complete(nil)

	err := receiveRunResult(t, runResult)
	if err == nil || !strings.Contains(err.Error(), "server stopped unexpectedly") {
		t.Fatalf("Run() error = %v, want unexpected-stop error", err)
	}
}

func TestRunShutsDownConnectionPublisherBeforeHTTPServer(t *testing.T) {
	t.Parallel()

	var events eventLog
	server := newFakeLifecycle("server", nil)
	server.events = &events
	publisher := newFakeLifecycle("publisher", nil)
	publisher.events = &events
	application := testApp(server, publisher)

	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		runResult <- application.Run(ctx)
	}()

	<-server.started
	<-publisher.started
	cancel()

	if err := <-runResult; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := events.snapshot(); len(got) != 2 || got[0] != "publisher" || got[1] != "server" {
		t.Fatalf("shutdown order = %v, want [publisher server]", got)
	}
	assertClosed(t, server.waitReturned, "server Wait")
	assertClosed(t, publisher.waitReturned, "publisher Wait")
}

func TestRunUsesOneDeadlineForAllComponentShutdowns(t *testing.T) {
	t.Parallel()

	server := newFakeLifecycle("server", nil)
	publisher := newFakeLifecycle("publisher", nil)
	deadlines := make(chan time.Time, 2)

	publisher.shutdownFunc = func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			return errors.New("publisher shutdown context has no deadline")
		}
		deadlines <- deadline
		<-ctx.Done()
		return ctx.Err()
	}
	server.shutdownFunc = func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			return errors.New("server shutdown context has no deadline")
		}
		deadlines <- deadline
		return ctx.Err()
	}

	application := testApp(server, publisher)
	application.shutdownTimeout = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		runResult <- application.Run(ctx)
	}()

	<-server.started
	<-publisher.started
	startedAt := time.Now()
	cancel()

	err := receiveRunResult(t, runResult)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want shutdown deadline", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("shutdown elapsed = %s, want one bounded deadline", elapsed)
	}

	firstDeadline := <-deadlines
	secondDeadline := <-deadlines
	if !firstDeadline.Equal(secondDeadline) {
		t.Fatalf("shutdown deadlines differ: %s and %s", firstDeadline, secondDeadline)
	}
	assertEventuallyClosed(t, server.waitReturned, "server Wait")
	assertEventuallyClosed(t, publisher.waitReturned, "publisher Wait")
}

func testApp(components ...*fakeLifecycle) *App {
	lifecycles := make([]component, 0, len(components))
	for _, current := range components {
		lifecycles = append(lifecycles, component{name: current.name, lifecycle: current})
	}

	return &App{
		components:      lifecycles,
		shutdownTimeout: time.Second,
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

type fakeLifecycle struct {
	name         string
	startErr     error
	waitErr      error
	shutdownErr  error
	blockStart   bool
	shutdownFunc func(context.Context) error
	started      chan struct{}
	done         chan struct{}
	waitReturned chan struct{}
	closeOnce    sync.Once
	waitOnce     sync.Once
	mu           sync.Mutex
	shutdowns    int
	events       *eventLog
}

func newFakeLifecycle(name string, startErr error) *fakeLifecycle {
	return &fakeLifecycle{
		name:         name,
		startErr:     startErr,
		started:      make(chan struct{}),
		done:         make(chan struct{}),
		waitReturned: make(chan struct{}),
	}
}

func (f *fakeLifecycle) Start(ctx context.Context) error {
	close(f.started)
	if f.blockStart {
		<-ctx.Done()
		return ctx.Err()
	}

	return f.startErr
}

func (f *fakeLifecycle) Wait() error {
	<-f.done
	f.waitOnce.Do(func() {
		close(f.waitReturned)
	})

	f.mu.Lock()
	defer f.mu.Unlock()
	return f.waitErr
}

func (f *fakeLifecycle) Shutdown(ctx context.Context) error {
	f.mu.Lock()
	f.shutdowns++
	f.mu.Unlock()
	if f.events != nil {
		f.events.append(f.name)
	}

	var err error
	if f.shutdownFunc != nil {
		err = f.shutdownFunc(ctx)
	} else {
		err = f.shutdownErr
	}
	f.complete(nil)

	return err
}

func (f *fakeLifecycle) complete(err error) {
	f.closeOnce.Do(func() {
		f.mu.Lock()
		f.waitErr = err
		f.mu.Unlock()
		close(f.done)
	})
}

func (f *fakeLifecycle) shutdownCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shutdowns
}

func receiveRunResult(t *testing.T, result <-chan error) error {
	t.Helper()

	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("App.Run did not return")
		return nil
	}
}

func assertClosed(t *testing.T, channel <-chan struct{}, name string) {
	t.Helper()

	select {
	case <-channel:
	default:
		t.Fatalf("%s had not returned before App.Run", name)
	}
}

func assertEventuallyClosed(t *testing.T, channel <-chan struct{}, name string) {
	t.Helper()

	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatalf("%s remained blocked after deadline cleanup", name)
	}
}

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) append(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}
