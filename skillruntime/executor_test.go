//go:build linux

package skillruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalExecutorRejectsInvalidEntrypoint(t *testing.T) {
	if validLimits(ExecutorLimits{TimeoutSeconds: 1, MemoryMiB: 512, CPUMilliCores: 0, MaxOutputBytes: 1024}) {
		t.Fatal("CPU must be explicitly bounded")
	}
	e := planExecutor(t)
	_, err := e.Execute(context.Background(), Execution{IsolationProfile: LocalProfile, Entrypoint: "../bin/sh", Limits: ExecutorLimits{1, 512, 1000, 1024}})
	if err == nil {
		t.Fatal("outside entrypoint accepted")
	}
	if err := os.Mkdir(filepath.Join(e.root, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.root, "bin", "provider"), []byte("#!/bin/sh\nprintf unsafe"), 0755); err != nil {
		t.Fatal(err)
	}
	_, err = e.Execute(context.Background(), Execution{IsolationProfile: LocalProfile, RuntimeDir: e.root, Entrypoint: "./bin/provider", Limits: ExecutorLimits{1, 512, 1000, 1024}})
	if err == nil || !strings.Contains(err.Error(), "INVALID_MANIFEST") {
		t.Fatalf("script entrypoint was not rejected: %v", err)
	}
}

// Opt-in real engine acceptance: the publisher payload is outside the checkout.
func TestLocalYARAIntegration(t *testing.T) {
	payload := os.Getenv("POGO_SKILL_RUNTIME_TEST_PAYLOAD")
	enabled := os.Getenv("POGO_SKILL_RUNTIME_TEST_LOCAL")
	if payload == "" || enabled != "1" {
		t.Skip("set POGO_SKILL_RUNTIME_TEST_PAYLOAD and POGO_SKILL_RUNTIME_TEST_LOCAL=1 for real local sandbox acceptance")
	}
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "runtime")
	if err := os.CopyFS(runtimeDir, os.DirFS(payload)); err != nil {
		t.Fatal(err)
	}
	executor, err := NewLocalExecutor(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "cmd", "pogo-yara-provider", "contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	identity := ContractIdentity{ID: "malware.file.yara.scan", Version: "1.0.0", Digest: bytesDigest(data)}
	rules := filepath.Join(root, "rules")
	if err := os.Mkdir(rules, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rules, "rules.yar"), []byte(`rule PogoPrototypeMarker { strings: $a = "POGO_SKILL_RUNTIME_SAFE_FIXTURE" condition: $a }`), 0444); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, text string
		count      int
	}{{"hit", "POGO_SKILL_RUNTIME_SAFE_FIXTURE", 1}, {"clean", "ordinary harmless text", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			content := []byte(tc.text)
			req := ProcessRequest{ProtocolVersion: "pogo.tool/1", CallID: "call:integration", Type: "invoke", Operation: "file.yara.scan", Contract: &identity, Input: json.RawMessage(`{"fileId":"file:test"}`),
				Context: &ProcessContext{Files: []ProcessFile{{InputPointer: "/fileId", FileID: "file:test", Digest: bytesDigest(content)}}, Assets: []ProcessAsset{{AssetID: "yara-rules", Digest: "sha256:" + strings.Repeat("a", 64)}}}}
			out, err := executor.Execute(context.Background(), Execution{IsolationProfile: LocalProfile, RuntimeDir: runtimeDir, Entrypoint: "./bin/pogo-yara-provider", Assets: map[string]string{"yara-rules": rules}, Files: map[string][]byte{"/fileId": content}, Request: req, Limits: ExecutorLimits{30, 512, 1000, 1048576}})
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Type   string
				Output struct{ Matches []struct{ Rule string } }
				Error  any
			}
			if err := json.Unmarshal(out, &response); err != nil {
				t.Fatal(err)
			}
			if response.Type != "result" || len(response.Output.Matches) != tc.count {
				t.Fatalf("unexpected real YARA result: %s", out)
			}
		})
	}
}
