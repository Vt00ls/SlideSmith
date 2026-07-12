//go:build linux

package service

import "golang.org/x/sys/unix"

func atomicExchangeDirectories(stagedPath, canonicalPath string) error {
	return unix.Renameat2(unix.AT_FDCWD, stagedPath, unix.AT_FDCWD, canonicalPath, unix.RENAME_EXCHANGE)
}
