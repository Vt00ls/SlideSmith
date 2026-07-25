//go:build linux

package taskworkspace

import "golang.org/x/sys/unix"

func renameEntryNoReplaceAt(fromDirectory int, from string, toDirectory int, to string) error {
	return unix.Renameat2(fromDirectory, from, toDirectory, to, unix.RENAME_NOREPLACE)
}
