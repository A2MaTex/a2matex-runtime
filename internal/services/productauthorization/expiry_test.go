package productauthorization

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/services/productworkflow"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

func start(c *Controller, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	return done
}

func end(t *testing.T, c *Controller, done <-chan error) {
	t.Helper()
	c.End()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMonitorRefreshEndAndDuplicateRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock, owner := setup(t)
		owner.quiesce = func(context.Context, string, uint64, constants.ProductQuiesceMode) (Quiescence, error) {
			t.Error("refresh called quiesce")
			return Quiescence{}, errors.New("unexpected quiesce")
		}
		done := start(c, t.Context())
		defer end(t, c, done)
		synctest.Wait()
		if err := c.Run(t.Context()); !errors.Is(err, xerror.ErrProductAuthorizationConfiguration) {
			t.Fatal("duplicate monitor", err)
		}
		// An early timer cannot transition the controller.
		clock.mu.Lock()
		for timer := range clock.timers {
			timer.ch <- clock.now
			delete(clock.timers, timer)
		}
		clock.mu.Unlock()
		synctest.Wait()
		if c.Snapshot().Phase != constants.ProductAuthorizationActive {
			t.Fatal("early timer refreshed grant")
		}
		clock.set(c.Snapshot().RefreshAt)
		synctest.Wait()
		decision(t, c, 5000, constants.ProductAttemptAllow)
		if c.Snapshot().RenewalReason != constants.ProductRenewalScheduled {
			t.Fatal("refresh not observed")
		}
		clock.mu.Lock()
		count := len(clock.timers)
		clock.mu.Unlock()
		if count != 1 {
			t.Fatal("unbounded/missing monitor timers", count)
		}
	})
	c, clock, _ := setup(t)
	c.End()
	c.End()
	if err := c.Run(t.Context()); err != nil || len(clock.timers) != 0 {
		t.Fatal("End before Run started monitor", err)
	}
}

func TestStandardClockMonitorsRefreshAndExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, _, owner := setup(t)
		c, err := New(grant(t), owner, pauseFunc(confirmed), Options{})
		if err != nil {
			t.Fatal(err)
		}
		done := start(c, t.Context())
		defer end(t, c, done)
		synctest.Wait()
		time.Sleep(time.Until(c.Snapshot().RefreshAt))
		synctest.Wait()
		if c.Snapshot().Phase != constants.ProductAuthorizationRenewalRequired {
			t.Fatal("standard clock missed refresh")
		}
		time.Sleep(time.Until(c.Snapshot().ExpiresAt))
		synctest.Wait()
		if c.Snapshot().Phase != constants.ProductAuthorizationExpired {
			t.Fatal("standard clock missed hard expiry")
		}
	})
}

func TestBudgetPauseOnceThenSafeExpiryReleasesOnlyDefinition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock, owner := setup(t)
		raw := json.RawMessage(`{"protected":"config"}`)
		doc := &ir.Document{Nodes: []ir.Node{{Config: ir.Object{"value": raw}}}}
		loaded := &productworkflow.Loaded{Document: doc, RuntimeIRHash: "protected-hash"}
		inputs := map[string]string{"input": "preserved"}
		variables := map[string]string{"score": "42"}
		pendingRetryDue := clock.Now().Add(time.Second)
		var modes []constants.ProductQuiesceMode
		var writes, releases int
		quiesce := owner.quiesce
		owner.quiesce = func(ctx context.Context, id string, generation uint64, mode constants.ProductQuiesceMode) (Quiescence, error) {
			modes = append(modes, mode)
			if loaded.Document == nil || c.Snapshot().Phase == constants.ProductAuthorizationExpired {
				t.Error("definition cleared or expiry published before quiescence")
			}
			return quiesce(ctx, id, generation, mode)
		}
		c.writer = pauseFunc(func(ctx context.Context, req execution.GroupControlRequest) (execution.GroupControlResult, error) {
			writes++
			if loaded.Document == nil {
				t.Error("definition cleared before pause confirmation")
			}
			return confirmed(ctx, req)
		})
		owner.release = func(context.Context, string, uint64) error {
			if writes != 2 || c.Snapshot().Phase != constants.ProductAuthorizationExpiring {
				t.Error("release order/phase")
			}
			releases++
			loaded.Close() // All controlled borrowers have joined.
			return nil
		}
		done := start(c, t.Context())
		defer end(t, c, done)
		decision(t, c, 60_000, constants.ProductAttemptHold)
		synctest.Wait()
		decision(t, c, 1, constants.ProductAttemptHold)
		synctest.Wait()
		if writes != 1 || releases != 0 || loaded.Document == nil {
			t.Fatal("budget pause repeated/released definition")
		}
		clock.set(c.Snapshot().ExpiresAt)
		synctest.Wait()
		if c.Snapshot().Phase != constants.ProductAuthorizationExpired || releases != 1 || len(modes) != 2 ||
			modes[0] != constants.ProductQuiesceBudget || modes[1] != constants.ProductQuiesceExpiry || loaded.Document != nil || len(doc.Nodes) != 0 {
			t.Fatal("expiry not settled in order")
		}
		for _, b := range raw {
			if b != 0 {
				t.Fatal("protected byte alias retained")
			}
		}
		if inputs["input"] != "preserved" || variables["score"] != "42" || pendingRetryDue.After(clock.Now()) {
			t.Fatal("live state lost or retry due reset")
		}
		decision(t, c, 1, constants.ProductAttemptExpired)
		synctest.Wait()
		if writes != 2 || releases != 1 {
			t.Fatal("repeated settled expiry")
		}
	})
}

func TestHardExpiryInterruptsBudgetCoordination(t *testing.T) {
	for _, blocked := range []string{"quiesce", "pause"} {
		t.Run(blocked, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, clock, owner := setup(t)
				quiesce := owner.quiesce
				var budgetJoined, expiryCalled, released bool
				owner.quiesce = func(ctx context.Context, id string, generation uint64, mode constants.ProductQuiesceMode) (Quiescence, error) {
					if mode == constants.ProductQuiesceBudget && blocked == "quiesce" {
						<-ctx.Done()
						budgetJoined = true
						return Quiescence{}, ctx.Err()
					}
					if mode == constants.ProductQuiesceExpiry {
						expiryCalled = true
						if !budgetJoined {
							t.Error("expiry abandoned original callback")
						}
					}
					return quiesce(ctx, id, generation, mode)
				}
				c.writer = pauseFunc(func(ctx context.Context, req execution.GroupControlRequest) (execution.GroupControlResult, error) {
					if blocked == "pause" && !expiryCalled {
						<-ctx.Done()
						budgetJoined = true
						return execution.GroupControlResult{}, ctx.Err()
					}
					return confirmed(ctx, req)
				})
				owner.release = func(context.Context, string, uint64) error { released = true; return nil }
				done := start(c, t.Context())
				defer end(t, c, done)
				decision(t, c, 60_000, constants.ProductAttemptHold)
				synctest.Wait()
				clock.set(c.Snapshot().ExpiresAt)
				synctest.Wait()
				if !expiryCalled || !released || c.Snapshot().Phase != constants.ProductAuthorizationExpired {
					t.Fatal("blocked budget did not switch to expiry")
				}
			})
		})
	}
}

func TestUnsafeExpiryFailsWithoutRelease(t *testing.T) {
	for _, name := range []string{"unjoined", "empty", "stale", "duplicate", "active status", "wrong group", "unknown pause", "already unconfirmed", "bad cursor", "wrong changed", "write failure", "release failure"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, clock, owner := setup(t)
				quiesce := owner.quiesce
				owner.quiesce = func(ctx context.Context, id string, generation uint64, mode constants.ProductQuiesceMode) (Quiescence, error) {
					r, err := quiesce(ctx, id, generation, mode)
					switch name {
					case "unjoined":
						err = errors.New("borrowers not joined")
					case "empty":
						r.Members = nil
					case "stale":
						r.Generation++
					case "duplicate":
						r.Members = append(r.Members, r.Members[0])
					case "active status":
						r.Members[0].Status = constants.ExecutionSucceeded
					}
					return r, err
				}
				c.writer = pauseFunc(func(ctx context.Context, req execution.GroupControlRequest) (execution.GroupControlResult, error) {
					r, err := confirmed(ctx, req)
					switch name {
					case "wrong group":
						r.RunGroupID = uuid.NewString()
					case "unknown pause":
						r.Disposition = constants.GroupControlStale
					case "already unconfirmed":
						r.Disposition = constants.GroupControlAlreadyTarget
					case "bad cursor":
						r.Cursor = ""
					case "wrong changed":
						r.Changed++
					case "write failure":
						err = xerror.ErrExecutionCommitUnknown
					}
					return r, err
				})
				var releases, failures int
				owner.release = func(context.Context, string, uint64) error { releases++; return errors.New("release failed") }
				owner.fail = func(error) { failures++ }
				clock.set(c.Snapshot().ExpiresAt)
				err := c.Run(t.Context())
				if err == nil || failures != 1 || c.Snapshot().Phase != constants.ProductAuthorizationEnded ||
					(name == "release failure" && releases != 1) || (name != "release failure" && releases != 0) {
					t.Fatal("unsafe expiry acknowledged", err, failures, releases)
				}
				if _, err := c.BeforeAttempt(t.Context(), 1); err == nil {
					t.Fatal("fatal controller restored permission")
				}
			})
		})
	}
}

func TestAlreadyPausedAndTerminalOwnership(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{true: "terminal", false: "user paused"}[terminal], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, clock, owner := setup(t)
				owner.quiesce = func(_ context.Context, id string, generation uint64, _ constants.ProductQuiesceMode) (Quiescence, error) {
					r := Quiescence{ExecutionID: id, Generation: generation, Terminal: terminal}
					if !terminal {
						r.PauseConfirmed = true
						r.Members = []execution.Boundary{{DeviceExecutionID: uuid.NewString(), NextNodeID: "next", Status: constants.ExecutionPaused}}
					}
					return r, nil
				}
				var writes, releases int
				c.writer = pauseFunc(func(_ context.Context, req execution.GroupControlRequest) (execution.GroupControlResult, error) {
					writes++
					return execution.GroupControlResult{RunGroupID: req.RunGroupID, Disposition: constants.GroupControlAlreadyTarget}, nil
				})
				owner.release = func(context.Context, string, uint64) error { releases++; return nil }
				clock.set(c.Snapshot().ExpiresAt)
				done := start(c, t.Context())
				synctest.Wait()
				if terminal && (writes != 0 || releases != 0 || c.Snapshot().Phase != constants.ProductAuthorizationEnded) ||
					!terminal && (writes != 1 || releases != 1 || c.Snapshot().Phase != constants.ProductAuthorizationExpired) {
					t.Fatal("terminal/user-pause ownership violated")
				}
				end(t, c, done)
			})
		})
	}
}

func TestEndAndOwnerLossJoinPendingCallbacks(t *testing.T) {
	for _, ownerLoss := range []bool{false, true} {
		t.Run(map[bool]string{true: "owner loss", false: "End"}[ownerLoss], func(t *testing.T) {
			for _, mode := range []constants.ProductQuiesceMode{constants.ProductQuiesceBudget, constants.ProductQuiesceExpiry} {
				t.Run(string(mode), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						c, clock, owner := setup(t)
						var joined bool
						owner.quiesce = func(ctx context.Context, _ string, _ uint64, _ constants.ProductQuiesceMode) (Quiescence, error) {
							<-ctx.Done()
							joined = true
							return Quiescence{}, ctx.Err()
						}
						owner.release = func(context.Context, string, uint64) error { t.Error("stop released borrowed graph"); return nil }
						owner.fail = func(error) { t.Error("expected stop classified as failure") }
						ctx, cancel := context.WithCancel(t.Context())
						defer cancel()
						if mode == constants.ProductQuiesceExpiry {
							clock.set(c.Snapshot().ExpiresAt)
						}
						done := start(c, ctx)
						if mode == constants.ProductQuiesceBudget {
							decision(t, c, 60_000, constants.ProductAttemptHold)
						}
						synctest.Wait()
						if ownerLoss {
							cancel()
						} else {
							c.End()
						}
						if err := <-done; err != nil || !joined {
							t.Fatal("pending callback not joined", err)
						}
					})
				})
			}
		})
	}
}

func TestSettlementDeadlineAndConcurrentChecks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock, owner := setup(t)
		owner.quiesce = func(ctx context.Context, _ string, _ uint64, _ constants.ProductQuiesceMode) (Quiescence, error) {
			<-ctx.Done()
			return Quiescence{}, ctx.Err()
		}
		var failures atomic.Int32
		owner.fail = func(error) { failures.Add(1) }
		clock.set(c.Snapshot().ExpiresAt)
		done := start(c, t.Context())
		err := <-done // synctest advances the bounded real settlement deadline.
		if !errors.Is(err, context.DeadlineExceeded) || failures.Load() != 1 {
			t.Fatal("settlement deadline not fatal", err)
		}
	})
	c, clock, _ := setup(t)
	done := start(c, t.Context())
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			for range 20 {
				_, _ = c.BeforeAttempt(t.Context(), 1)
				_ = c.Snapshot()
			}
		})
	}
	clock.set(c.Snapshot().RefreshAt)
	c.End()
	wg.Wait()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	clock.mu.Lock()
	count := len(clock.timers)
	clock.mu.Unlock()
	if count != 0 {
		t.Fatal("monitor leaked timers", count)
	}
}

func TestEarlierOwnerDeadlineWinsSettlementTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, clock, owner := setup(t)
		owner.quiesce = func(ctx context.Context, _ string, _ uint64, _ constants.ProductQuiesceMode) (Quiescence, error) {
			<-ctx.Done()
			return Quiescence{}, ctx.Err()
		}
		owner.fail = func(error) { t.Error("owner deadline became authorization failure") }
		clock.set(c.Snapshot().ExpiresAt)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := <-start(c, ctx); err != nil {
			t.Fatal("owner stop lost authority", err)
		}
	})
}

func TestOwnerLossFencesDispatchBeforeMonitorAcknowledgement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _, _ := setup(t)
		ctx, cancel := context.WithCancel(t.Context())
		done := start(c, ctx)
		defer end(t, c, done)
		synctest.Wait()
		permit := decision(t, c, 1, constants.ProductAttemptAllow)
		cancel()
		got, err := c.BeforeDispatch(t.Context(), 1, permit)
		if err != nil || got.Kind != constants.ProductAttemptEnded {
			t.Fatal("owner cancellation still allowed dispatch", got.Kind, err)
		}
	})
}

func TestIgnoredSettlementCancellationFencesButJoinsOriginalBorrower(t *testing.T) {
	for _, mode := range []constants.ProductQuiesceMode{constants.ProductQuiesceBudget, constants.ProductQuiesceExpiry} {
		t.Run(string(mode), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, clock, owner := setup(t)
				release := make(chan struct{})
				quiesce := owner.quiesce
				owner.quiesce = func(ctx context.Context, id string, generation uint64, mode constants.ProductQuiesceMode) (Quiescence, error) {
					<-release // Intentionally broken owner ignores ctx until externally joined.
					return quiesce(ctx, id, generation, mode)
				}
				var failures int
				owner.fail = func(error) { failures++ }
				owner.release = func(context.Context, string, uint64) error { t.Error("cleared unjoined graph"); return nil }
				if mode == constants.ProductQuiesceBudget {
					decision(t, c, 60_000, constants.ProductAttemptHold)
				} else {
					clock.set(c.Snapshot().ExpiresAt)
				}
				done := start(c, t.Context())
				synctest.Wait()
				if mode == constants.ProductQuiesceBudget {
					clock.set(c.Snapshot().ExpiresAt)
					synctest.Wait()
				}
				time.Sleep(constants.CheckpointTimeout)
				synctest.Wait()
				if failures != 1 || c.Snapshot().Phase != constants.ProductAuthorizationEnded {
					t.Fatal("uncooperative callback did not raise fatal fence")
				}
				select {
				case <-done:
					t.Fatal("abandoned original borrower")
				default:
				}
				close(release)
				if err := <-done; !errors.Is(err, context.DeadlineExceeded) || failures != 1 {
					t.Fatal("join lost first fatal cause", err)
				}
			})
		})
	}
}
