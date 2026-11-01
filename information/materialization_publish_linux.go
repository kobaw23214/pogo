//go:build linux

package information

import (
	"golang.org/x/sys/unix"
	"os"
	"path"
)

func publishMaterialization(root *os.Root, staging, destination string) error {
	parent, err := root.Open("uploads/information")
	if err != nil {
		return err
	}
	defer parent.Close()
	return unix.Renameat2(int(parent.Fd()), path.Base(staging), int(parent.Fd()), path.Base(destination), unix.RENAME_NOREPLACE)
}
