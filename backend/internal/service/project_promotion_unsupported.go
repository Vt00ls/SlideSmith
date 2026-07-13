//go:build !darwin && !linux

package service

import (
	"context"
	"fmt"
)

func acquireProjectPromotionLock(context.Context, string) (func(), error) {
	return nil, fmt.Errorf("atomic runtime project promotion is unsupported on this platform")
}

func atomicExchangeDirectories(string, string) error {
	return fmt.Errorf("atomic runtime project promotion is unsupported on this platform")
}

func copyProjectDirectoryStrict(context.Context, string, string) error {
	return fmt.Errorf("strict runtime project copying is unsupported on this platform")
}
