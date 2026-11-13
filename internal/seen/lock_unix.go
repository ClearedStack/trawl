//go:build unix

package seen

import (
	"os"
	"syscall"
)

// lock takes an exclusive advisory lock, released when the file is closed.
func lock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
