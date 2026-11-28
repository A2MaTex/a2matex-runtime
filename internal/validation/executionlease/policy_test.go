package executionlease

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func TestTimeBoundaries(t *testing.T) {
	base := fixture(t).FixedClock.Unix()
	cases := []struct {
		name          string
		elapsed, skew time.Duration
		change        func(map[string]any)
		accept        bool
	}{
		{"normal", 0, 0, nil, true},
		{"just before expiry", 1800*time.Second - time.Nanosecond, 30 * time.Second, nil, true},
		{"at expiry", 1800 * time.Second, 30 * time.Second, nil, false},
		{"after expiry", 1800*time.Second + time.Nanosecond, 30 * time.Second, nil, false},
		{"refresh already due", 1201 * time.Second, 0, nil, true},
		{"future within skew", 0, 30 * time.Second, func(m map[string]any) { m["issuedAt"] = base + 30; m["notBefore"] = base + 30 }, true},
		{"future beyond skew", 0, 30 * time.Second, func(m map[string]any) { m["issuedAt"] = base + 31; m["notBefore"] = base + 31 }, false},
		{"not before beyond skew", 0, 30 * time.Second, func(m map[string]any) { m["notBefore"] = base + 31 }, false},
		{"zero tolerance", 0, 0, func(m map[string]any) { m["issuedAt"] = base + 1; m["notBefore"] = base + 1 }, false},
		{"ordering", 0, 0, func(m map[string]any) { m["refreshAfter"] = m["expiresAt"] }, false},
		{"not before ordering", 0, 0, func(m map[string]any) { m["notBefore"] = base - 1 }, false},
		{"maximum lifetime", 0, 0, func(m map[string]any) { m["expiresAt"] = base + constants.ExecutionLeaseMaxSeconds }, true},
		{"excess lifetime", 0, 0, func(m map[string]any) { m["expiresAt"] = base + constants.ExecutionLeaseMaxSeconds + 1 }, false},
		{"distant timestamp safe", 0, 0, func(m map[string]any) { m["expiresAt"] = constants.ExecutionLeaseMaxTimestamp }, false},
		{"maximum sequence initial", 0, 0, func(m map[string]any) { m["sequence"] = constants.ExecutionLeaseMaxSequence }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, elapsed, key, m := signing(t)
			v.skew = c.skew
			*elapsed = c.elapsed
			if c.change != nil {
				c.change(m)
			}
			r, err := v.Verify(context.Background(), Request{Compact: sign(t, key, m), Mode: constants.LeaseVerificationInitial, Expected: expected(m)})
			if !c.accept {
				assertFailure(t, r, err, xerror.ErrExecutionLeaseInvalid)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			expiry := time.Unix(r.Grant.Claims().ExpiresAt, 0)
			if !r.Grant.ExpiresAt().Equal(expiry) || !r.Grant.RefreshAt().Equal(time.Unix(r.Grant.Claims().RefreshAfter, 0)) {
				t.Fatal("arrival changed signed deadlines")
			}
		})
	}
}

func TestMonotonicDeadlinesAndFinalFences(t *testing.T) {
	v, _, key, m := signing(t)
	// Use a real monotonic anchor while fixed elapsed time represents the only
	// clock consulted after startup; the process's current wall time is irrelevant.
	v.anchor = time.Now()
	base := v.anchor.Unix()
	m["issuedAt"] = base
	m["notBefore"] = base
	m["refreshAfter"] = base + 60
	m["expiresAt"] = base + 120
	grant := initial(t, v, key, m)
	if !strings.Contains(grant.ExpiresAt().String(), "m=") {
		t.Fatal("expiry lost monotonic component")
	}
	original := grant.ExpiresAt()
	elapsed := 30 * time.Second
	v.since = func(time.Time) time.Duration { return elapsed }
	r, err := v.Verify(context.Background(), Request{Compact: sign(t, key, m), Mode: constants.LeaseVerificationRenew, Expected: expected(m), Current: grant})
	if err != nil || !r.Replayed || r.Grant.ExpiresAt() != original {
		t.Fatalf("replay deadline: %v", err)
	}
	copy := grant.Claims()
	copy.ExpiresAt++
	if grant.Claims().ExpiresAt != base+120 {
		t.Fatal("claims accessor aliases state")
	}
	for _, value := range []any{v, grant, grant.Claims(), r, Request{Compact: "PRIVATE", Current: grant}, expected(m), Options{VerificationKeys: map[string]string{"PRIVATE": "PRIVATE"}}} {
		if string(jsonBytes(t, value)) != "{}" {
			t.Fatal("authorization data serialized")
		}
	}
	for _, stage := range []string{"candidate expired", "current expired", "cancel during policy", "cancel at return"} {
		t.Run(stage, func(t *testing.T) {
			v, _, key, m := signing(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := Request{Compact: sign(t, key, m), Mode: constants.LeaseVerificationInitial, Expected: expected(m)}
			if stage == "current expired" {
				request.Current = initial(t, v, key, m)
				request.Mode = constants.LeaseVerificationRenew
				m["leaseId"] = uuid.NewString()
				m["sequence"] = 2
				m["refreshAfter"] = float64(m["refreshAfter"].(float64)) + 60
				m["expiresAt"] = m["expiresAt"].(float64) + 60
				request.Compact = sign(t, key, m)
			}
			calls := 0
			v.since = func(time.Time) time.Duration {
				calls++
				if strings.HasPrefix(stage, "cancel") && ((stage == "cancel during policy" && calls == 1) || (stage == "cancel at return" && calls == 2)) {
					cancel()
				}
				if calls > 1 && !strings.HasPrefix(stage, "cancel") {
					return 1800 * time.Second
				}
				return 0
			}
			r, err := v.Verify(ctx, request)
			want := xerror.ErrExecutionLeaseInvalid
			if strings.HasPrefix(stage, "cancel") {
				want = context.Canceled
			}
			assertFailure(t, r, err, want)
		})
	}
}

func TestRenewalAndReauthorizationPredicates(t *testing.T) {
	cases := []struct {
		name   string
		mode   constants.LeaseVerificationMode
		change func(map[string]any)
		accept bool
	}{
		{"advancing", constants.LeaseVerificationRenew, nil, true},
		{"skip sequence", constants.LeaseVerificationRenew, func(m map[string]any) { m["sequence"] = 100 }, true},
		{"maximum sequence", constants.LeaseVerificationRenew, func(m map[string]any) { m["sequence"] = constants.ExecutionLeaseMaxSequence }, true},
		{"same sequence", constants.LeaseVerificationRenew, func(m map[string]any) { m["sequence"] = 1 }, false},
		{"same lease id", constants.LeaseVerificationRenew, func(m map[string]any) { m["leaseId"] = "77777777-7777-4777-8777-777777777777" }, false},
		{"user change", constants.LeaseVerificationRenew, func(m map[string]any) { m["userId"] = uuid.NewString() }, false},
		{"entitlement change", constants.LeaseVerificationRenew, func(m map[string]any) { m["entitlementId"] = uuid.NewString() }, false},
		{"family change", constants.LeaseVerificationRenew, func(m map[string]any) { m["leaseFamilyId"] = uuid.NewString() }, false},
		{"session change", constants.LeaseVerificationRenew, func(m map[string]any) { m["runtimeSessionId"] = uuid.NewString() }, false},
		{"thumbprint change", constants.LeaseVerificationRenew, func(m map[string]any) { m["runtimeKeyThumbprint"] = strings.Repeat("b", 64) }, false},
		{"expiry not advancing", constants.LeaseVerificationRenew, func(m map[string]any) { m["expiresAt"] = float64(fixture(t).FixedClock.Unix() + 1800) }, false},
		{"refresh not advancing", constants.LeaseVerificationRenew, func(m map[string]any) { m["refreshAfter"] = float64(fixture(t).FixedClock.Unix() + 1200) }, false},
		{"issue decreases", constants.LeaseVerificationRenew, func(m map[string]any) { m["issuedAt"] = m["issuedAt"].(float64) - 1 }, false},
		{"not before decreases", constants.LeaseVerificationRenew, func(m map[string]any) {
			m["issuedAt"] = m["issuedAt"].(float64) - 1
			m["notBefore"] = m["notBefore"].(float64) - 1
		}, false},
		{"fresh reauthorization", constants.LeaseVerificationReauthorize, nil, true},
		{"reauthorization same session", constants.LeaseVerificationReauthorize, func(m map[string]any) { m["runtimeSessionId"] = "66666666-6666-4666-8666-666666666666" }, false},
		{"reauthorization same key", constants.LeaseVerificationReauthorize, func(m map[string]any) { m["runtimeKeyThumbprint"] = fixture(t).Claims["runtimeKeyThumbprint"] }, false},
		{"reauthorization changed product", constants.LeaseVerificationReauthorize, func(m map[string]any) { m["productId"] = uuid.NewString() }, false},
		{"reauthorization changed release", constants.LeaseVerificationReauthorize, func(m map[string]any) { m["releaseId"] = uuid.NewString() }, false},
		{"reauthorization changed package", constants.LeaseVerificationReauthorize, func(m map[string]any) { m["packageHash"] = strings.Repeat("b", 64) }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v, elapsed, key, m := signing(t)
			current := initial(t, v, key, m)
			m["leaseId"] = uuid.NewString()
			m["sequence"] = 2
			m["refreshAfter"] = m["refreshAfter"].(float64) + 60
			m["expiresAt"] = m["expiresAt"].(float64) + 60
			if c.mode == constants.LeaseVerificationReauthorize {
				*elapsed = 1800 * time.Second
				m["issuedAt"] = float64(v.anchor.Unix() + 1800)
				m["notBefore"] = m["issuedAt"]
				m["refreshAfter"] = float64(v.anchor.Unix() + 3000)
				m["expiresAt"] = float64(v.anchor.Unix() + 3600)
				m["runtimeSessionId"] = uuid.NewString()
				m["runtimeKeyThumbprint"] = strings.Repeat("b", 64)
			}
			if c.change != nil {
				c.change(m)
			}
			r, err := v.Verify(context.Background(), Request{Compact: sign(t, key, m), Mode: c.mode, Expected: expected(m), Current: current})
			if c.accept {
				if err != nil || r.Replayed || r.Grant == current {
					t.Fatalf("advancing grant: %v", err)
				}
				return
			}
			assertFailure(t, r, err, xerror.ErrExecutionLeaseInvalid)
		})
	}
	// Reauthorization cannot replace a live family, and renewal cannot revive it.
	for _, mode := range []constants.LeaseVerificationMode{constants.LeaseVerificationRenew, constants.LeaseVerificationReauthorize} {
		v, elapsed, key, m := signing(t)
		current := initial(t, v, key, m)
		if mode == constants.LeaseVerificationRenew {
			*elapsed = 1800 * time.Second
		}
		if mode == constants.LeaseVerificationReauthorize {
			m["runtimeSessionId"] = uuid.NewString()
			m["runtimeKeyThumbprint"] = strings.Repeat("b", 64)
		}
		m["leaseId"] = uuid.NewString()
		m["sequence"] = 2
		m["expiresAt"] = m["expiresAt"].(float64) + 60
		m["refreshAfter"] = m["refreshAfter"].(float64) + 60
		r, err := v.Verify(context.Background(), Request{Compact: sign(t, key, m), Mode: mode, Expected: expected(m), Current: current})
		assertFailure(t, r, err, xerror.ErrExecutionLeaseInvalid)
	}
}

func TestRequestTrustRotationAndConcurrency(t *testing.T) {
	v, _, key, m := signing(t)
	current := initial(t, v, key, m)
	other, _, otherKey, otherClaims := signing(t)
	foreign := initial(t, other, otherKey, otherClaims)
	for _, r := range []Request{
		{Mode: constants.LeaseVerificationRenew}, {Mode: constants.LeaseVerificationInitial, Expected: expected(m), Current: current},
		{Mode: constants.LeaseVerificationRenew, Expected: expected(m), Current: &Verified{}},
		{Mode: constants.LeaseVerificationRenew, Expected: expected(m), Current: foreign},
		{Mode: "PRIVATE", Expected: expected(m)},
	} {
		result, err := v.Verify(context.Background(), r)
		assertFailure(t, result, err, xerror.ErrExecutionLeaseConfiguration)
	}
	var absent *Verifier
	result, err := absent.Verify(context.Background(), Request{})
	assertFailure(t, result, err, xerror.ErrExecutionLeaseConfiguration)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = v.Verify(ctx, Request{})
	assertFailure(t, result, err, context.Canceled)
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	result, err = v.Verify(ctx, Request{})
	assertFailure(t, result, err, context.DeadlineExceeded)
	// Rotation is permitted for an advancing grant, never for changed same-sequence replay.
	public, newKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rotated, _ := fixedVerifier(t, map[string]string{"test-key": publicPEM(t, key.Public()), "next-key": publicPEM(t, public)}, 30*time.Second)
	old := initial(t, rotated, key, m)
	header := []byte(`{"alg":"EdDSA","kid":"next-key","typ":"a2matex-execution-lease+jws"}`)
	result, err = rotated.Verify(context.Background(), Request{Compact: signRaw(t, newKey, header, jsonBytes(t, m)), Mode: constants.LeaseVerificationRenew, Expected: expected(m), Current: old})
	assertFailure(t, result, err, xerror.ErrExecutionLeaseInvalid)
	m["leaseId"] = uuid.NewString()
	m["sequence"] = 2
	m["expiresAt"] = m["expiresAt"].(float64) + 60
	m["refreshAfter"] = m["refreshAfter"].(float64) + 60
	token := signRaw(t, newKey, header, jsonBytes(t, m))
	request := Request{Compact: token, Mode: constants.LeaseVerificationRenew, Expected: expected(m), Current: old}
	var wg sync.WaitGroup
	failures := make(chan error, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := rotated.Verify(context.Background(), request)
			if err != nil {
				failures <- err
			} else if result.Replayed || result.Grant.SigningKeyID() != "next-key" {
				failures <- errors.New("rotation result")
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}
