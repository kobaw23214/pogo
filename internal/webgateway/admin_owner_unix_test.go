//go:build unix

package webgateway

import (
	"os"
	"syscall"
	"testing"
)

type ownerFileInfo struct {
	os.FileInfo
	metadata any
}

func (i ownerFileInfo) Sys() any { return i.metadata }

func TestSocketOwnedByProcessChecksCurrentAndForeignUID(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := socketOwnedByProcess(info); err != nil {
		t.Fatal(err)
	}
	foreign := ownerFileInfo{FileInfo: info, metadata: &syscall.Stat_t{Uid: uint32(os.Geteuid() + 1)}}
	if err := socketOwnedByProcess(foreign); err == nil {
		t.Fatal("another UID was accepted")
	}
	unknown := ownerFileInfo{FileInfo: info, metadata: nil}
	if err := socketOwnedByProcess(unknown); err == nil {
		t.Fatal("unverifiable ownership was accepted")
	}
}
