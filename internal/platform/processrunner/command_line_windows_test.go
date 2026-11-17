package processrunner

import (
	"strings"
	"testing"
	"time"
)

func TestCommandLineLimitCountsEscapingAndUTF16(t *testing.T) {
	const executable = `C:\Test Tools\adb.exe`
	const lastArg = `"😀`
	// Quoted executable, two spaces, escaped quote, surrogate pair and NUL.
	overhead := len(executable) + 2 + 2 + 2 + 2 + 1
	arg := strings.Repeat("x", windowsCommandLineLimit-overhead)
	if !commandLineFits(executable, []string{arg, lastArg}) {
		t.Fatal("command line at the Windows limit was rejected")
	}
	if commandLineFits(executable, []string{arg + "x", lastArg}) {
		t.Fatal("oversized Windows command line was accepted")
	}
	result := Run(t.Context(), executable, []string{arg + "x", lastArg}, time.Second)
	if result.Outcome != CommandLineTooLong {
		t.Fatalf("outcome = %s, want %s", result.Outcome, CommandLineTooLong)
	}
	// ADB's POSIX encoding expands each literal apostrophe to four characters.
	encodedText := "'" + strings.Repeat("'\\''", 10_000) + "'"
	if commandLineFits(executable, []string{"-s", "emulator-5554", "shell", "-T", "input", "text", encodedText}) {
		t.Fatal("oversized encoded ADB command was accepted")
	}
}
