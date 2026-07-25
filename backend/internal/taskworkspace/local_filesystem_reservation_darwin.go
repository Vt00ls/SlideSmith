//go:build darwin

package taskworkspace

import (
	"os"

	"golang.org/x/sys/unix"
)

func physicallyReserveFile(file *os.File, size int64) error {
	if size == 0 {
		return nil
	}
	allocation := &unix.Fstore_t{
		Flags: unix.F_ALLOCATEALL, Posmode: unix.F_PEOFPOSMODE, Length: size,
	}
	if err := unix.FcntlFstore(file.Fd(), unix.F_PREALLOCATE, allocation); err != nil {
		return err
	}
	return file.Truncate(size)
}
