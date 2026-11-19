package switcher

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const firstRule = "bfbb31bc-d4b5-44ec-b9eb-a09d68e74083"
const secondRule = "db505e3c-49b6-47e5-bd5d-f2a935bef173"

func TestComparisons(t *testing.T) {
	for _, tc := range []struct{ op, yesLeft, yesRight, noLeft, noRight string }{
		{"EQ", "1", "1.0", "1", "2"}, {"NE", `"a"`, `"b"`, `"a"`, `"a"`},
		{"LT", "1.5", "2", "2", "2"}, {"LTE", "2", "2", "3", "2"},
		{"GT", "2", "1.5", "2", "2"}, {"GTE", "2", "2", "1", "2"},
		{"CONTAINS", `"abc"`, `"b"`, `"abc"`, `"B"`},
		{"NOT_CONTAINS", `"abc"`, `"B"`, `"abc"`, `"b"`},
		{"STARTS_WITH", `"abc"`, `"ab"`, `"abc"`, `"bc"`},
		{"ENDS_WITH", `"abc"`, `"bc"`, `"abc"`, `"ab"`},
		{"IS_EMPTY", `""`, "", `" "`, ""}, {"IS_NOT_EMPTY", `" "`, "", `""`, ""},
		{"IS_TRUE", "true", "", "false", ""}, {"IS_FALSE", "false", "", "true", ""},
	} {
		t.Run(tc.op, func(t *testing.T) {
			for _, match := range []bool{true, false} {
				l, r := tc.noLeft, tc.noRight
				if match {
					l, r = tc.yesLeft, tc.yesRight
				}
				got, err := compare(tc.op, []byte(l), []byte(r))
				if err != nil || got != match {
					t.Fatalf("comparison: %v %v, want %v", got, err, match)
				}
			}
		})
	}
	for _, tc := range []struct {
		op, l, r string
		want     bool
	}{
		{"EQ", "true", "true", true}, {"EQ", `"10"`, `"10"`, true},
		{"EQ", "-0", "0", true}, {"NE", "1", "1.0", false},
		{"CONTAINS", `"abc"`, `""`, true}, {"STARTS_WITH", `""`, `""`, true},
		{"ENDS_WITH", `"abc"`, `""`, true}, {"NOT_CONTAINS", `"abc"`, `""`, false},
	} {
		got, err := compare(tc.op, []byte(tc.l), []byte(tc.r))
		if err != nil || got != tc.want {
			t.Fatalf("%+v: %v %v", tc, got, err)
		}
	}
	for _, tc := range []struct{ op, l, r string }{
		{"NE", "1", `"1"`}, {"EQ", "true", "1"}, {"LT", `"1"`, `"2"`},
		{"EQ", "null", "null"}, {"EQ", "[]", "[]"}, {"EQ", "{}", "{}"},
		{"EQ", "1e999", "1"}, {"EQ", "NaN", "1"}, {"IS_EMPTY", "true", ""},
		{"IS_TRUE", `"true"`, ""}, {"CONTAINS", "1", "2"},
	} {
		_, err := compare(tc.op, []byte(tc.l), []byte(tc.r))
		var command xerror.CommandError
		if !errors.As(err, &command) || command.Code != constants.CommandInputUnsupported || command.Retryable {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func invocation(rules string) registry.Invocation {
	return registry.Invocation{Config: map[string]json.RawMessage{"rules": []byte(rules)}}
}

func TestOrderedLazyEvaluation(t *testing.T) {
	rules := `[{"id":"` + firstRule + `","operator":"LT"},{"id":"` + secondRule + `","operator":"LT"}]`
	for _, tc := range []struct {
		left, port string
		rightCalls int
	}{
		{"30", firstRule, 1}, {"75", secondRule, 2}, {"150", "default", 2},
	} {
		t.Run(tc.left, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			call := invocation(rules)
			leftCalls, rightCalls := 0, 0
			call.ResolveInput = func(got context.Context, field string) (json.RawMessage, error) {
				if got != ctx {
					t.Fatal("attempt context replaced")
				}
				if field == "value" {
					leftCalls++
					return []byte(tc.left), nil
				}
				rightCalls++
				if field == firstRule {
					return []byte("50"), nil
				}
				return []byte("100"), nil
			}
			var selected string
			call.SelectPort = func(_ context.Context, port string) error { selected = port; return nil }
			out, err := Invoke(ctx, call)
			if err != nil || out == nil || len(out) != 0 || selected != tc.port || leftCalls != 1 || rightCalls != tc.rightCalls {
				t.Fatalf("%v %v selected=%s left=%d right=%d", out, err, selected, leftCalls, rightCalls)
			}
		})
	}
	for _, reached := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		call := invocation(rules)
		call.ResolveInput = func(_ context.Context, field string) (json.RawMessage, error) {
			if field == "value" {
				return []byte("30"), nil
			}
			if field == firstRule {
				if reached {
					return []byte("10"), nil
				}
				return []byte("50"), nil
			}
			return nil, xerror.CommandError{Code: constants.CommandInputUnsupported, Message: "Previous node output is unavailable"}
		}
		call.SelectPort = func(context.Context, string) error { return nil }
		out, err := Invoke(ctx, call)
		cancel()
		if (err != nil) != reached || (out == nil) != reached {
			t.Fatalf("reached=%v: %v %v", reached, out, err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	call := invocation(`[{"id":"` + secondRule + `","operator":"IS_TRUE"},{"id":"` + firstRule + `","operator":"IS_TRUE"}]`)
	call.ResolveInput = func(_ context.Context, field string) (json.RawMessage, error) {
		if field != "value" {
			t.Fatal("unary operand resolved")
		}
		return []byte("true"), nil
	}
	call.SelectPort = func(_ context.Context, port string) error {
		if port != secondRule {
			t.Fatal("array order lost")
		}
		return nil
	}
	if _, err := Invoke(ctx, call); err != nil {
		t.Fatal(err)
	}
}

func TestGuardsAndCancellation(t *testing.T) {
	for _, rules := range []string{"null", "[]", `{}`, `[{"id":"bad","operator":"EQ"}]`,
		`[{"id":"` + firstRule + `","operator":"REGEX"}]`,
		`[{"id":"` + firstRule + `","operator":"EQ"},{"id":"` + firstRule + `","operator":"EQ"}]`,
		`[` + strings.Repeat(`{"id":"`+firstRule+`","operator":"EQ"},`, constants.MaxSwitchRules) + `{}]`,
	} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		call := invocation(rules)
		call.ResolveInput = func(context.Context, string) (json.RawMessage, error) {
			t.Fatal("invalid config resolved an input")
			return nil, nil
		}
		call.SelectPort = func(context.Context, string) error { t.Fatal("invalid config routed"); return nil }
		_, err := Invoke(ctx, call)
		cancel()
		if !errors.Is(err, xerror.ErrInvocationInvalid) {
			t.Fatalf("guard: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	call := invocation(`[{"id":"` + firstRule + `","operator":"IS_TRUE"}]`)
	if _, err := Invoke(ctx, call); !errors.Is(err, xerror.ErrInvocationInvalid) {
		t.Fatal(err)
	}
	call.ResolveInput = func(context.Context, string) (json.RawMessage, error) { cancel(); return []byte("true"), nil }
	call.SelectPort = func(context.Context, string) error { t.Fatal("cancelled evaluation selected a port"); return nil }
	if out, err := Invoke(ctx, call); out != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("%v %v", out, err)
	}
}
