package processrunner

import "syscall"

const windowsCommandLineLimit = 32_767

func commandLineFits(executable string, args []string) bool {
	encoded, err := syscall.UTF16FromString(syscall.EscapeArg(executable))
	if err != nil {
		return false
	}
	units := len(encoded)
	for _, arg := range args {
		encoded, err := syscall.UTF16FromString(syscall.EscapeArg(arg))
		if err != nil {
			return false
		}
		// The per-argument NUL counts as a separator for all but the final argument.
		units += len(encoded)
		if units > windowsCommandLineLimit {
			return false
		}
	}
	return units <= windowsCommandLineLimit
}
