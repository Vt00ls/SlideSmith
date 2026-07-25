//go:build !linux && !darwin

package taskworkspace

import "os"

func physicallyReserveFile(_ *os.File, _ int64) error {
	return ErrLocalFilesystemUnavailable
}
