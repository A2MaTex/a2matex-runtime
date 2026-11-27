package productauthorization

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/validation"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/executionlease"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Every case uses a fresh signed J6 grant. No production trust, historical
// expired fixture or unverified claim map is injected into the controller.
func grant(t *testing.T) *executionlease.Verified {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := executionlease.New(executionlease.Options{VerificationKeys: map[string]string{
		"test": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
	}})
	if err != nil {
		t.Fatal(err)
	}
	expected := executionlease.Expected{ExecutionID: uuid.NewString(), ProductID: uuid.NewString(), ReleaseID: uuid.NewString(),
		RuntimeSessionID: uuid.NewString(), PackageHash: strings.Repeat("a", 64), RuntimeKeyThumbprint: strings.Repeat("b", 64)}
	now := time.Now().Unix()
	payload, err := json.Marshal(map[string]any{
		"leaseId": uuid.NewString(), "leaseFamilyId": uuid.NewString(), "executionId": expected.ExecutionID, "sequence": 1,
		"entitlementId": uuid.NewString(), "userId": uuid.NewString(), "productId": expected.ProductID, "releaseId": expected.ReleaseID,
		"packageHash": expected.PackageHash, "runtimeSessionId": expected.RuntimeSessionID, "runtimeKeyThumbprint": expected.RuntimeKeyThumbprint,
		"issuedAt": now - 1, "notBefore": now - 1, "refreshAfter": now + 30, "expiresAt": now + 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	header := []byte(`{"alg":"EdDSA","typ":"a2matex-execution-lease+jws","kid":"test"}`)
	encoded := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	compact := encoded + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(encoded)))
	verified, err := verifier.Verify(t.Context(), executionlease.Request{Compact: compact, Expected: expected, Mode: constants.LeaseVerificationInitial})
	if err != nil {
		t.Fatal(err)
	}
	return verified.Grant
}

type manualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*manualTimer]time.Time
}
type manualTimer struct {
	clock *manualClock
	ch    chan time.Time
}

func (c *manualClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *manualClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &manualTimer{clock: c, ch: make(chan time.Time, 1)}
	if d <= 0 {
		timer.ch <- c.now
	} else {
		c.timers[timer] = c.now.Add(d)
	}
	return timer
}
func (t *manualTimer) C() <-chan time.Time { return t.ch }
func (t *manualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	_, found := t.clock.timers[t]
	delete(t.clock.timers, t)
	return found
}
func (c *manualClock) set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
	for timer, deadline := range c.timers {
		if !now.Before(deadline) {
			timer.ch <- now
			delete(c.timers, timer)
		}
	}
}

type controlledOwner struct {
	quiesce func(context.Context, string, uint64, constants.ProductQuiesceMode) (Quiescence, error)
	release func(context.Context, string, uint64) error
	fail    func(error)
}

func (o *controlledOwner) Quiesce(ctx context.Context, id string, generation uint64, mode constants.ProductQuiesceMode) (Quiescence, error) {
	return o.quiesce(ctx, id, generation, mode)
}
func (o *controlledOwner) ReleaseDefinition(ctx context.Context, id string, generation uint64) error {
	if o.release != nil {
		return o.release(ctx, id, generation)
	}
	return nil
}
func (o *controlledOwner) Fail(err error) {
	if o.fail != nil {
		o.fail(err)
	}
}

type pauseFunc func(context.Context, execution.GroupControlRequest) (execution.GroupControlResult, error)

func (f pauseFunc) Pause(ctx context.Context, request execution.GroupControlRequest) (execution.GroupControlResult, error) {
	return f(ctx, request)
}

func confirmed(_ context.Context, request execution.GroupControlRequest) (execution.GroupControlResult, error) {
	return execution.GroupControlResult{RunGroupID: request.RunGroupID, Disposition: constants.GroupControlCommitted, Changed: len(request.Members), Cursor: "1"}, nil
}

func setup(t *testing.T) (*Controller, *manualClock, *controlledOwner) {
	t.Helper()
	g := grant(t)
	clock := &manualClock{now: g.RefreshAt().Add(-30 * time.Second), timers: make(map[*manualTimer]time.Time)}
	owner := &controlledOwner{quiesce: func(_ context.Context, id string, generation uint64, _ constants.ProductQuiesceMode) (Quiescence, error) {
		return Quiescence{ExecutionID: id, Generation: generation, Members: []execution.Boundary{
			{DeviceExecutionID: uuid.NewString(), NextNodeID: "next", Status: constants.ExecutionQueued},
		}}, nil
	}}
	c, err := New(g, owner, pauseFunc(confirmed), Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	return c, clock, owner
}

func decision(t *testing.T, c *Controller, timeout int64, want constants.ProductAttemptDecision) Decision {
	t.Helper()
	d, err := c.BeforeAttempt(t.Context(), timeout)
	if err != nil || d.Kind != want {
		t.Fatalf("decision = %s, %v; want %s", d.Kind, err, want)
	}
	return d
}

func TestNewAndCheckGuards(t *testing.T) {
	g := grant(t)
	var typedOwner *controlledOwner
	var typedWriter pauseFunc
	var typedClock *manualClock
	for _, test := range []struct {
		name string
		g    *executionlease.Verified
		o    Owner
		w    PauseWriter
		opt  Options
	}{
		{"nil grant", nil, &controlledOwner{}, pauseFunc(confirmed), Options{}},
		{"zero grant", &executionlease.Verified{}, &controlledOwner{}, pauseFunc(confirmed), Options{}},
		{"nil owner", g, nil, pauseFunc(confirmed), Options{}},
		{"typed owner", g, typedOwner, pauseFunc(confirmed), Options{}},
		{"typed writer", g, &controlledOwner{}, typedWriter, Options{}},
		{"typed clock", g, &controlledOwner{}, pauseFunc(confirmed), Options{Clock: typedClock}},
		{"zero clock", g, &controlledOwner{}, pauseFunc(confirmed), Options{Clock: &manualClock{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.g, test.o, test.w, test.opt); !errors.Is(err, xerror.ErrProductAuthorizationConfiguration) {
				t.Fatal(err)
			}
		})
	}
	c, _, _ := setup(t)
	for _, timeout := range []int64{0, -1, validation.NodeTimeoutMaxMS + 1} {
		if _, err := c.BeforeAttempt(t.Context(), timeout); !errors.Is(err, xerror.ErrProductAuthorizationConfiguration) {
			t.Fatal("invalid timeout", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.BeforeAttempt(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestExactEntryBudgetDoesNotMutateRenewal(t *testing.T) {
	for _, delta := range []time.Duration{0, -time.Nanosecond, -7 * time.Second} {
		t.Run(delta.String(), func(t *testing.T) {
			c, clock, _ := setup(t)
			before := c.Snapshot()
			clock.set(before.ExpiresAt.Add(-7*time.Second - delta))
			err := c.CheckInitial(t.Context(), 5000)
			if delta == 0 && err != nil || delta != 0 && !errors.Is(err, xerror.ErrExecutionLeaseInvalid) {
				t.Fatal("entry boundary", err)
			}
			if c.Snapshot() != before {
				t.Fatal("pre-admission check changed renewal/hold")
			}
		})
	}
}

func TestRefreshBudgetHoldAndHardFence(t *testing.T) {
	for _, scheduledFirst := range []bool{false, true} {
		t.Run(map[bool]string{true: "scheduled first", false: "budget first"}[scheduledFirst], func(t *testing.T) {
			c, clock, _ := setup(t)
			if scheduledFirst {
				clock.set(c.Snapshot().RefreshAt)
				decision(t, c, 1, constants.ProductAttemptAllow)
			}
			decision(t, c, 60_000, constants.ProductAttemptHold)
			state := c.Snapshot()
			want := constants.ProductRenewalAttemptBudget
			if scheduledFirst {
				want = constants.ProductRenewalScheduled
			}
			if state.RenewalReason != want || !state.BudgetBlocked {
				t.Fatal("reason/hold", state.Phase, state.RenewalReason)
			}
			decision(t, c, 1, constants.ProductAttemptHold)
			if c.Snapshot() != state {
				t.Fatal("repeated hold changed state")
			}
			clock.set(state.ExpiresAt)
			decision(t, c, 1, constants.ProductAttemptExpired)
			if c.Snapshot().Phase != constants.ProductAuthorizationExpiring {
				t.Fatal("gate pretended expiry already settled")
			}
			c.End()
			decision(t, c, 1, constants.ProductAttemptEnded)
		})
	}
}

func TestBeforeDispatchRechecksAuthority(t *testing.T) {
	c, clock, _ := setup(t)
	permit := decision(t, c, 5000, constants.ProductAttemptAllow)
	clock.set(c.Snapshot().RefreshAt)
	if got, err := c.BeforeDispatch(t.Context(), 5000, permit); err != nil || got.Kind != constants.ProductAttemptAllow || got.Revision <= permit.Revision {
		t.Fatal("benign refresh rejected fresh authority", got.Kind, err)
	}
	clock.set(c.Snapshot().ExpiresAt.Add(-7*time.Second + time.Nanosecond))
	if got, err := c.BeforeDispatch(t.Context(), 5000, permit); err != nil || got.Kind != constants.ProductAttemptHold {
		t.Fatal("persistence stall bypassed budget", got.Kind, err)
	}
	other, _, _ := setup(t)
	for _, invalid := range []Decision{{}, {Kind: constants.ProductAttemptAllow, Generation: 1, Revision: 1}, decision(t, other, 1, constants.ProductAttemptAllow)} {
		if _, err := c.BeforeDispatch(t.Context(), 1, invalid); !errors.Is(err, xerror.ErrProductAuthorizationConfiguration) {
			t.Fatal("invalid authority", err)
		}
	}
	clock.set(c.Snapshot().ExpiresAt)
	if got, err := c.BeforeDispatch(t.Context(), 1, permit); err != nil || got.Kind != constants.ProductAttemptExpired {
		t.Fatal("stale decision dispatched", got.Kind, err)
	}
}

func TestClockFailureAndPrivateSerialization(t *testing.T) {
	c, clock, _ := setup(t)
	d := decision(t, c, 1, constants.ProductAttemptAllow)
	for _, value := range []any{d, c.Snapshot(), Quiescence{ExecutionID: c.Snapshot().ExecutionID}} {
		raw, err := json.Marshal(value)
		if err != nil || string(raw) != "{}" {
			t.Fatal("internal data has public JSON", string(raw), err)
		}
	}
	clock.set(clock.Now().Add(-time.Nanosecond))
	if _, err := c.BeforeAttempt(t.Context(), 1); !errors.Is(err, xerror.ErrProductAuthorizationConfiguration) {
		t.Fatal("backward clock permitted", err)
	} else {
		var op *xerror.OperationError
		if !errors.As(err, &op) || op.Detail == "" || strings.Contains(err.Error(), c.Snapshot().ExecutionID) {
			t.Fatal("missing safe context")
		}
	}
	clock.set(c.Snapshot().RefreshAt)
	if _, err := c.BeforeAttempt(t.Context(), 1); err == nil {
		t.Fatal("broken clock restored authorization")
	}
}
