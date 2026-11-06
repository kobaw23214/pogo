//go:build !unix

package webgateway

import (
	"errors"
	"os"
)

func socketOwnedByProcess(os.FileInfo) error {
	return socketManagementSupported()
}

func socketManagementSupported() error {
	return errors.New("gateway: private Unix management sockets are unsupported on this platform")
}
