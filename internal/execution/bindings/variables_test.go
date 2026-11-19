package bindings_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/A2MaTex/a2matex-runtime/internal/execution"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/bindings"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/execution/variables"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
	"github.com/google/uuid"
)

func variableView(group, device string, lookup bindings.VariableLookup) bindings.VariableView {
	return bindings.VariableView{RunGroupID: group, DeviceExecutionID: device, Lookup: lookup}
}

func TestVariableViewIdentity(t *testing.T) {
	group, device := uuid.NewString(), uuid.NewString()
	lookup := func(context.Context, constants.VariableScope, string) (json.RawMessage, bool, error) {
		t.Fatal("identity validation read data")
		return nil, false, nil
	}
	for _, tc := range []struct {
		name  string
		view  bindings.VariableView
		valid bool
	}{
		{"absent", bindings.VariableView{}, true},
		{"owned", variableView(group, device, lookup), true},
		{"lazy nil lookup", variableView(group, device, nil), true},
		{"foreign group", variableView(uuid.NewString(), device, lookup), false},
		{"foreign device", variableView(group, uuid.NewString(), lookup), false},
		{"missing metadata", bindings.VariableView{Lookup: lookup}, false},
		{"invalid metadata", variableView("bad", device, nil), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.view.ValidFor(group, device); got != tc.valid {
				t.Fatalf("valid=%v", got)
			}
		})
	}
}

func TestVariableResolutionAndOwnedValues(t *testing.T) {
	r := resolver(t)
	group, a, b := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store, err := variables.New(group, []string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	seed := func(device string, scope constants.VariableScope, name, value string) {
		t.Helper()
		_, err := store.Apply(t.Context(), execution.VariableWrite{AttemptRef: execution.AttemptRef{RunGroupID: group, DeviceExecutionID: device, AttemptID: uuid.NewString(), NodeID: "seed"}, Scope: scope, Name: name, Value: []byte(value)}, func(context.Context) (execution.CheckpointResult, error) {
			return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "1"}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	seed(a, constants.VariableScopeFlow, "message", `"Hello A"`)
	seed(b, constants.VariableScopeFlow, "message", `"Hello B"`)
	seed(a, constants.VariableScopeRunGroup, "sharedText", `"Shared"`)
	for _, device := range []string{a, b} {
		bound := device
		values := bindings.Values{Variables: variableView(group, bound, func(ctx context.Context, scope constants.VariableScope, name string) (json.RawMessage, bool, error) {
			return store.Get(ctx, bound, scope, name)
		})}
		for _, source := range []string{constants.InputSourceFlowVariable, constants.InputSourceGlobalVariable} {
			name, want := "message", `"Hello A"`
			if bound == b {
				want = `"Hello B"`
			}
			if source == constants.InputSourceGlobalVariable {
				name, want = "sharedText", `"Shared"`
			}
			node := inputNode("android.input_text", "text", []byte(`{"source":"`+source+`","name":"`+name+`"}`))
			got, err := r.Resolve(t.Context(), node, "text", values)
			if err != nil || string(got) != want {
				t.Fatalf("%s %v", got, err)
			}
			got[1] = 'X'
			again, err := r.Resolve(t.Context(), node, "text", values)
			if err != nil || string(again) != want {
				t.Fatal("returned value changed storage", err)
			}
		}
	}
	for _, tc := range []struct{ name, kind, field, value string }{
		{"false", "flow.set_variable", "value", "false"}, {"zero", "flow.delay", "durationMs", "0"}, {"empty", "flow.set_variable", "value", `""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed(a, constants.VariableScopeFlow, tc.name, tc.value)
			view := variableView(group, a, func(ctx context.Context, scope constants.VariableScope, name string) (json.RawMessage, bool, error) {
				return store.Get(ctx, a, scope, name)
			})
			got, err := r.Resolve(t.Context(), inputNode(tc.kind, tc.field, []byte(`{"source":"FLOW_VARIABLE","name":"`+tc.name+`"}`)), tc.field, bindings.Values{Variables: view})
			if err != nil || string(got) != tc.value {
				t.Fatalf("%s %v", got, err)
			}
		})
	}
}

func TestVariableShapeKindAndValueFailures(t *testing.T) {
	r := resolver(t)
	calls := 0
	view := variableView(uuid.NewString(), uuid.NewString(), func(context.Context, constants.VariableScope, string) (json.RawMessage, bool, error) {
		calls++
		return []byte(`"value"`), true, nil
	})
	for _, raw := range []string{`{"source":"FLOW_VARIABLE"}`, `{"source":"FLOW_VARIABLE","name":null}`, `{"source":"FLOW_VARIABLE","name":1}`, `{"source":"FLOW_VARIABLE","name":" bad "}`, `{"source":"FLOW_VARIABLE","name":"` + strings.Repeat("x", 101) + `"}`, `{"source":"GLOBAL_VARIABLE","name":"value","runGroupId":"other"}`, `{"source":"FLOW_VARIABLE","name":"value","deviceExecutionId":"other"}`, `{"source":"FLOW_VARIABLE","name":"value","fallback":0}`} {
		_, err := r.Resolve(t.Context(), inputNode("android.input_text", "text", []byte(raw)), "text", bindings.Values{Variables: view})
		failure(t, err, "text", "binding_invalid", "resolve_input_binding")
	}
	node := inputNode("android.tap", "x", []byte(`{"source":"FLOW_VARIABLE","name":"value"}`))
	node.Config = ir.Object{"targetMode": []byte(`"ABSOLUTE_PIXELS"`)}
	_, err := r.Resolve(t.Context(), node, "x", bindings.Values{Variables: view})
	failure(t, err, "x", "source_unsupported", "resolve_input_binding")
	if calls != 0 {
		t.Fatal("invalid source/destination invoked reader")
	}
	for _, tc := range []struct {
		name, value string
		found       bool
		reason      string
	}{
		{"missing", "", false, "variable_missing"}, {"number into text", "75", true, "input_value_invalid"},
		{"null", "null", true, "input_value_invalid"}, {"invalid scalar", "{}", true, "input_value_invalid"},
		{"invalid utf8", string([]byte{0xff}), true, "input_value_invalid"}, {"too long", `"` + strings.Repeat("x", 10001) + `"`, true, "input_value_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view.Lookup = func(context.Context, constants.VariableScope, string) (json.RawMessage, bool, error) {
				return []byte(tc.value), tc.found, nil
			}
			_, err := r.Resolve(t.Context(), inputNode("android.input_text", "text", []byte(`{"source":"FLOW_VARIABLE","name":"value"}`)), "text", bindings.Values{Variables: view})
			failure(t, err, "text", tc.reason, "resolve_input_binding")
		})
	}
}

func TestVariableReaderErrorsAndOptionalContext(t *testing.T) {
	r := resolver(t)
	node := inputNode("android.input_text", "text", []byte(`{"source":"FLOW_VARIABLE","name":"value"}`))
	for _, view := range []bindings.VariableView{{}, variableView(uuid.NewString(), uuid.NewString(), nil), variableView("bad", uuid.NewString(), func(context.Context, constants.VariableScope, string) (json.RawMessage, bool, error) {
		t.Fatal("invalid metadata read")
		return nil, false, nil
	})} {
		_, err := r.Resolve(t.Context(), node, "text", bindings.Values{Variables: view})
		var command xerror.CommandError
		if !errors.Is(err, xerror.ErrVariableInvalid) || errors.As(err, &command) {
			t.Fatal("missing context became a publisher error", err)
		}
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, xerror.ErrVariableUnavailable, xerror.ErrVariableInvalid, xerror.ErrExecutionPersistence, errors.New("private callback error")} {
		view := variableView(uuid.NewString(), uuid.NewString(), func(context.Context, constants.VariableScope, string) (json.RawMessage, bool, error) {
			return nil, false, cause
		})
		_, err := r.Resolve(t.Context(), node, "text", bindings.Values{Variables: view})
		var command xerror.CommandError
		if !errors.Is(err, cause) || errors.As(err, &command) {
			t.Fatal("reader error reclassified", err)
		}
	}
	group, device := uuid.NewString(), uuid.NewString()
	store, err := variables.New(group, []string{device})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	view := variableView(group, device, func(ctx context.Context, scope constants.VariableScope, name string) (json.RawMessage, bool, error) {
		return store.Get(ctx, device, scope, name)
	})
	_, err = r.Resolve(t.Context(), node, "text", bindings.Values{Variables: view})
	if !errors.Is(err, xerror.ErrVariableUnavailable) {
		t.Fatal(err)
	}
	got, err := r.Resolve(t.Context(), inputNode("android.input_text", "text", literal(`"plain"`)), "text", bindings.Values{})
	if err != nil || string(got) != `"plain"` {
		t.Fatal("ordinary resolution required a variable store", err)
	}
}

func TestVariableReadWaitsForPublicationAndCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := resolver(t)
		group, device := uuid.NewString(), uuid.NewString()
		store, err := variables.New(group, []string{device})
		if err != nil {
			t.Fatal(err)
		}
		entered, release := make(chan struct{}), make(chan struct{})
		applied := make(chan error, 1)
		go func() {
			_, err := store.Apply(t.Context(), execution.VariableWrite{AttemptRef: execution.AttemptRef{RunGroupID: group, DeviceExecutionID: device, NodeID: "seed", AttemptID: uuid.NewString()}, Scope: constants.VariableScopeFlow, Name: "message", Value: []byte(`"published"`)}, func(context.Context) (execution.CheckpointResult, error) {
				close(entered)
				<-release
				return execution.CheckpointResult{Disposition: constants.CheckpointCommitted, Sequence: 1, Cursor: "1"}, nil
			})
			applied <- err
		}()
		<-entered
		view := variableView(group, device, func(ctx context.Context, scope constants.VariableScope, name string) (json.RawMessage, bool, error) {
			return store.Get(ctx, device, scope, name)
		})
		node := inputNode("android.input_text", "text", []byte(`{"source":"FLOW_VARIABLE","name":"message"}`))
		ctx, cancel := context.WithCancel(t.Context())
		cancelled := make(chan error, 1)
		go func() { _, err := r.Resolve(ctx, node, "text", bindings.Values{Variables: view}); cancelled <- err }()
		read := make(chan json.RawMessage, 1)
		go func() {
			value, err := r.Resolve(t.Context(), node, "text", bindings.Values{Variables: view})
			if err != nil {
				t.Error(err)
			}
			read <- value
		}()
		synctest.Wait()
		select {
		case <-read:
			t.Fatal("reader crossed publication gate")
		default:
		}
		cancel()
		if err := <-cancelled; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		close(release)
		if err := <-applied; err != nil {
			t.Fatal(err)
		}
		if value := <-read; string(value) != `"published"` {
			t.Fatalf("%s", value)
		}
		if err := store.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}
