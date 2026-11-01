package main

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/yosukeno/pogo/skillruntime"
)

// handleHash implements malware.file.hash: pure-Go digest computation over the
// verified input bytes. Fuzzy hashes are intentionally not claimed here.
func handleHash(req *skillruntime.ProcessRequest, raw json.RawMessage) (any, []artifact, *failErr) {
	var in struct {
		FileID       string `json:"fileId"`
		SampleSHA256 string `json:"sampleSha256"`
	}
	if err := decodeTypedStrict(raw, &in); err != nil {
		return nil, nil, fail("INVALID_INPUT", "input does not match the contract: %v", err)
	}
	_, data, ferr := inputFile(req, in.FileID, in.SampleSHA256)
	if ferr != nil {
		return nil, nil, ferr
	}
	s256 := sha256.Sum256(data)
	s1 := sha1.Sum(data)
	m5 := md5.Sum(data)
	return map[string]any{
		"sha256":    hex.EncodeToString(s256[:]),
		"sha1":      hex.EncodeToString(s1[:]),
		"md5":       hex.EncodeToString(m5[:]),
		"sizeBytes": len(data),
	}, nil, nil
}
