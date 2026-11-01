package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"regexp"
	"sync"
)

// digestPattern is the v1 digest format: "sha256:" + lowercase hex.
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var digestBuffers = sync.Pool{New: func() any {
	buffer := make([]byte, 1<<20)
	return &buffer
}}

// DigestBytes returns the v1 digest string ("sha256:<lowercase hex>") of b.
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// IsDigest reports whether s has the exact v1 digest format.
func IsDigest(s string) bool { return digestPattern.MatchString(s) }

// digestFile streams a verified regular file, avoiding allocations proportional
// to native toolchain size. limit is a host bound, not an untrusted header;
// limit <= 0 means unlimited — the whole regular file is digested.
func digestFile(file *os.File, limit int64) (string, int64, error) {
	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() || (limit > 0 && info.Size() > limit) {
		return "", 0, fmt.Errorf("file is not a regular file within size limit %d", limit)
	}
	span := info.Size()
	if limit > 0 && limit+1 < span {
		span = limit + 1
	}
	h := sha256.New()
	buffer := digestBuffers.Get().(*[]byte)
	defer digestBuffers.Put(buffer)
	// Reuse a bounded large buffer for native bundles instead of allocating
	// a small buffer and issuing many Windows ReadAt calls for every file.
	n, err := io.CopyBuffer(h, struct{ io.Reader }{io.NewSectionReader(file, 0, span)}, *buffer)
	if err != nil {
		return "", n, err
	}
	if n != info.Size() || (limit > 0 && n > limit) {
		return "", n, fmt.Errorf("file size changed while computing digest")
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

// verifyDigest returns an error if DigestBytes(b) != want.
func verifyDigest(label string, b []byte, want string) error {
	if !IsDigest(want) {
		return fmt.Errorf("%s: malformed digest %q", label, want)
	}
	if got := DigestBytes(b); got != want {
		return fmt.Errorf("%s: digest mismatch: computed %s, declared %s", label, got, want)
	}
	return nil
}

// digestWriter hashes everything written through it.
type digestWriter struct {
	w io.Writer
	h hash.Hash
}

func newDigestWriter(w io.Writer) *digestWriter {
	return &digestWriter{w: w, h: sha256.New()}
}

func (d *digestWriter) Write(p []byte) (int, error) {
	d.h.Write(p)
	return d.w.Write(p)
}

func (d *digestWriter) sum() string {
	return "sha256:" + hex.EncodeToString(d.h.Sum(nil))
}
