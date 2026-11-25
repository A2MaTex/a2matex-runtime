// macOS (development) support: same pipe handling as owner_linux.go.

package discovery

import (
	"os"

	"golang.org/x/sys/unix"
)

func preventOwnerPipeInheritance(commands, ready *os.File) {
	unix.CloseOnExec(int(commands.Fd()))
	unix.CloseOnExec(int(ready.Fd()))
}
