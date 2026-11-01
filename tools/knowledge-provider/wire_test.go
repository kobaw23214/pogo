package main

import (
	"encoding/json"
	"strings"
	"os"
	"path/filepath"
	"testing"

	"github.com/yosukeno/pogo/skillruntime/core"
)

// The provider's frames must clear the host-side process-response schema —
// this is the contract the sandbox enforces, checked here so a drift fails
// in tests instead of as OUTPUT_INVALID in production.
func TestResponseFramesValidate(t *testing.T) {
	dir := t.TempDir()
	entries := filepath.Join(dir, "entries.json")
	stats := filepath.Join(dir, "stats.json")
	os.WriteFile(entries, []byte(fixtureAttck), 0o644)
	os.WriteFile(stats, []byte(fixtureTiweb), 0o644)
	// assetPath 是常量 /assets/...;为了让本测试走到文件,先保存/恢复可测路径。
	// 直接构造调用路径:attckLookup/stats 接收 path,绕开常量。
	out, err := attckLookup(entries, json.RawMessage(`{"query":"T1547.001"}`))
	if err != nil {
		t.Fatal(err)
	}
	frame := processResponse{ProtocolVersion: protocolVersion, CallID: "call:t", Type: "result", Output: out, Artifacts: &[]declaredArtifact{}}
	raw, _ := json.Marshal(frame)
	if err := core.ValidateDocument(core.KindProcessResponse, raw); err != nil {
		t.Fatalf("result frame invalid: %v", err)
	}
	// describe 帧(带钉定契约)
	pinned := attckContract
	pinned.Digest = "sha256:" + strings.Repeat("b", 64)
	dr := dispatch(processRequest{Type: "describe", Contract: &pinned})
	dr.ProtocolVersion = protocolVersion
	dr.CallID = "call:d"
	raw, _ = json.Marshal(dr)
	if err := core.ValidateDocument(core.KindProcessResponse, raw); err != nil {
		t.Fatalf("describe frame invalid: %v", err)
	}
	// 错误帧
	er := errReply(&toolError{code: "ASSET_MISSING", message: "no file"})
	er.ProtocolVersion, er.CallID = protocolVersion, "call:e"
	raw, _ = json.Marshal(er)
	if err := core.ValidateDocument(core.KindProcessResponse, raw); err != nil {
		t.Fatalf("error frame invalid: %v", err)
	}
}
