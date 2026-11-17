package ir

import (
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		left, right string
		comparison  int
		valid       bool
	}{
		{"0.1.0", "0.1.0", 0, true}, {"0.10.0", "0.2.99", 1, true}, {"1.0.0", "2.0.0", -1, true},
		{strings.Repeat("9", 100) + ".0.0", "100.0.0", 1, true}, {"0.0.0-dev", "0.1.0", 0, false}, {"01.0.0", "1.0.0", 0, false},
	} {
		if got, valid := CompareVersions(tc.left, tc.right); got != tc.comparison || valid != tc.valid {
			t.Fatalf("%s vs %s: %d %v", tc.left, tc.right, got, valid)
		}
	}
}
