//go:build !windows

package processrunner

func commandLineFits(string, []string) bool { return true }
