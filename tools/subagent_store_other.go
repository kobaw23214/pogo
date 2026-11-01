//go:build !unix

package tools

import (
	"errors"
	"os"
)

func trySubagentContextLease(string) (*os.File, error) {
	return nil, errors.New("retained subagents require process-safe file locking on this platform")
}
