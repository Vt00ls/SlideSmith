//go:build darwin

package taskworkspace

import "golang.org/x/sys/unix"

func renameEntryNoReplaceAt(fromDirectory int, from string, toDirectory int, to string) error {
	return unix.RenameatxNp(fromDirectory, from, toDirectory, to, unix.RENAME_EXCL)
}
