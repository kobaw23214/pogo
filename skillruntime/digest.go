package skillruntime

import (
	"crypto/sha256"
	"encoding/hex"
)

func bytesDigest(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}
