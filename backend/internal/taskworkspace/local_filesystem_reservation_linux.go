//go:build linux

package taskworkspace

import (
	"os"

	"golang.org/x/sys/unix"
)

func physicallyReserveFile(file *os.File, size int64) error {
	if size == 0 {
		return nil
	}
	return unix.Fallocate(int(file.Fd()), 0, 0, size)
}
