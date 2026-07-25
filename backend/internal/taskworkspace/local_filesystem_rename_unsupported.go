//go:build !linux && !darwin

package taskworkspace

func renameEntryNoReplaceAt(_ int, _ string, _ int, _ string) error {
	return ErrLocalFilesystemUnavailable
}
