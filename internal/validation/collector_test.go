package validation

import (
	"fmt"
	"reflect"
	"testing"
)

func TestCollectorMatchesCanonicalCap(t *testing.T) {
	var all []Diagnostic
	var collector Collector
	for i := 149; i >= 0; i-- {
		finding := NewDiagnostic(CodeNodeValueBindingInvalid, Path{"nodes", i}, fmt.Sprintf("node%03d", i), "")
		all = append(all, finding)
		collector.Add(finding)
	}
	if got, want := collector.Result(), BuildResult(all, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("collector differs from full assembly: %+v", got)
	}
	result := collector.Result()
	result.Errors[0].Code = CodeNodeConfigInvalid
	if collector.Result().Errors[0].Code != CodeNodeValueBindingInvalid {
		t.Fatal("result aliases collector")
	}
}
