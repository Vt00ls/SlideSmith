//go:build darwin

package service

import "golang.org/x/sys/unix"

func atomicExchangeDirectories(stagedPath, canonicalPath string) error {
	return unix.RenamexNp(stagedPath, canonicalPath, unix.RENAME_SWAP)
}
