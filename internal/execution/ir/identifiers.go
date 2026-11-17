package ir

import (
	"regexp"
	"strings"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

var (
	identifier = regexp.MustCompile(constants.RuntimeIdentifierPattern)
	fieldName  = regexp.MustCompile(constants.WorkflowFieldNamePattern)
	variable   = regexp.MustCompile(constants.VariableNamePattern)
	code       = regexp.MustCompile(constants.RuntimeCodePattern)
	version    = regexp.MustCompile(constants.RuntimeVersionPattern)
)

func ValidIdentifier(value string) bool   { return identifier.MatchString(value) }
func ValidFieldName(value string) bool    { return fieldName.MatchString(value) }
func ValidVariableName(value string) bool { return variable.MatchString(value) }
func ValidCode(value string) bool {
	return len(value) <= constants.RuntimeCodeMaxLength && code.MatchString(value)
}
func ValidVersion(value string) bool { return version.MatchString(value) }

// CompareVersions compares strict release versions without overflowing numeric components.
func CompareVersions(left, right string) (int, bool) {
	if !ValidVersion(left) || !ValidVersion(right) {
		return 0, false
	}
	leftParts, rightParts := strings.Split(left, "."), strings.Split(right, ".")
	for index, part := range leftParts {
		other := rightParts[index]
		if len(part) < len(other) {
			return -1, true
		}
		if len(part) > len(other) {
			return 1, true
		}
		if result := strings.Compare(part, other); result != 0 {
			return result, true
		}
	}
	return 0, true
}
