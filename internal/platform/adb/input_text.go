package adb

import (
	"strings"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

// Validate checks whether Android's input text command can carry the text exactly.
func (c InputTextCommand) Validate() error {
	if len(c.Text) > constants.MaxAndroidInputTextLength {
		return inputTextError("text_too_long", "Input Text supports up to 10,000 printable ASCII characters")
	}
	for i := range len(c.Text) {
		if c.Text[i] < ' ' || c.Text[i] > '~' {
			return inputTextError("unsupported_character", "Input Text supports printable ASCII. Remove accented characters, emoji, tabs, and line breaks")
		}
	}
	if strings.Contains(c.Text, "%s") {
		return inputTextError("unsupported_percent_sequence", "Android treats %s as a space. Remove the literal %s sequence from the text")
	}
	return nil
}

func inputTextArgs(value string) []string {
	quoted := "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
	return []string{"shell", "-T", "input", "text", quoted}
}

func inputTextError(reason, message string) error {
	return &xerror.OperationError{
		Operation: "validate_input_text",
		Detail:    reason,
		Err:       xerror.CommandError{Code: constants.CommandInputUnsupported, Message: message},
	}
}
