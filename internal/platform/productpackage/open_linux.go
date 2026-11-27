package productpackage

import (
	"os"

	"golang.org/x/sys/unix"
)

func openPackageFile(root *os.Root, reference string) (*os.File, error) {
	// Nonblocking opening lets the caller reject a substituted FIFO using Stat.
	return root.OpenFile(reference, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
