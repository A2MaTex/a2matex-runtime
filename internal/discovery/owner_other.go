//go:build !linux && !darwin

package discovery

import "os"

// On Windows os/exec restricts inheritance to the handles it passes explicitly.
func preventOwnerPipeInheritance(_, _ *os.File) {}
