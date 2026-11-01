//go:build !unix

package information

import "os"

const materializationReuseSupported = false

// Without a cross-process lock, keep the original independent-copy behavior.
// No reusable cache or borrowed path is exposed on these platforms.
func lockMaterialization(_ *os.File) (func(), bool, error) {
	return func() {}, false, nil
}
