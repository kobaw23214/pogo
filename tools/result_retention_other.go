//go:build !unix && !windows

package tools

import (
	"context"
	"errors"
	"os"
)

func lockResultLifecycle(context.Context, *os.Root) (func(), error) {
	return nil, errors.New("stored result lifecycle requires process-safe file locking on this platform")
}
