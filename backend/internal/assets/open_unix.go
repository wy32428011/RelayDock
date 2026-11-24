//go:build unix

package assets

import (
	"os"
	"syscall"
)

// A regular file swapped for a FIFO must not hang resource verification.
const sourceOpenFlags = os.O_RDONLY | syscall.O_NONBLOCK
