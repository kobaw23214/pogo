//go:build !linux && !darwin

package information

import "os"

func publishMaterialization(root *os.Root, staging, destination string) error {
	// These platforms never borrow cached paths. Preserve independent creation;
	// a directory rename cannot overwrite a regular file or nonempty directory.
	return root.Rename(staging, destination)
}
