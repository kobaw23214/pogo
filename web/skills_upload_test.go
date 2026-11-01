package web

// Streaming upload tests: the compressed size is uncapped by default, an
// env-configured ceiling cuts the transfer, and the decompression ratio still
// stops adversarial archives — the three properties the 32MB rewrite traded
// for.

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/skills"
)

// bigSkillZip builds an in-memory skill bundle with an incompressible
// reference payload — the shape of a real data-bearing skill (media, archives,
// model files all refuse to compress), and the one the old 32MB ceiling
// refused. Random-ish bytes keep the compression ratio near 1x, under the
// bomb guard.
func bigSkillZip(t *testing.T, payloadMB int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("big-skill/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("---\nname: big-skill\ndescription: A bundle with a large reference payload.\n---\n\nSee the reference.\n")); err != nil {
		t.Fatal(err)
	}
	w, err = zw.Create("big-skill/references/data.bin")
	if err != nil {
		t.Fatal(err)
	}
	// A tiny LCG: deterministic, cheap, and no more compressible than noise.
	var state uint32 = 0x12345678
	chunk := make([]byte, 256*1024)
	for written := 0; written < payloadMB<<20; written += len(chunk) {
		for i := range chunk {
			state = state*1664525 + 1013904223
			chunk[i] = byte(state >> 16)
		}
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func postSkillZip(t *testing.T, h *harness, data []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/api/skills/upload", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/zip")
	res, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// 40MB of references passes with no ceiling configured (the default), and the
// staging file never lands in /tmp: the upload stage sits beside the skills
// directory, outside the loader's scan.
func TestSkillUploadStreamsWithNoDefaultCap(t *testing.T) {
	userDir := t.TempDir()
	h := newHarness(t, idleRespond, func(c *Config) {
		c.SkillOptions = skills.Options{UserDir: userDir}
	})

	data := bigSkillZip(t, 34) // 34MB of incompressible bytes, past the old 32MB cap
	res := postSkillZip(t, h, data)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("large upload: %d (%s)", res.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(userDir, "big-skill", "references", "data.bin")); err != nil {
		t.Fatalf("payload not installed: %v", err)
	}
	stageRoot := filepath.Join(filepath.Dir(userDir), ".skill-uploads")
	entries, err := os.ReadDir(stageRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging root %s not empty after install: %v", stageRoot, entries)
	}
}

// POGO_SKILL_UPLOAD_MAX_BYTES is the deployment's ceiling: a transfer past it
// is cut off, reported with the old wire shape, and leaves no staging debris.
func TestSkillUploadEnvCapCutsTheTransfer(t *testing.T) {
	userDir := t.TempDir()
	h := newHarness(t, idleRespond, func(c *Config) {
		c.SkillOptions = skills.Options{UserDir: userDir}
	})
	t.Setenv("POGO_SKILL_UPLOAD_MAX_BYTES", "1024")

	res := postSkillZip(t, h, bigSkillZip(t, 1))
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "zip exceeds") {
		t.Fatalf("capped upload: %d (%s), want 400 zip exceeds", res.StatusCode, body)
	}
	stageRoot := filepath.Join(filepath.Dir(userDir), ".skill-uploads")
	if entries, err := os.ReadDir(stageRoot); err != nil || len(entries) != 0 {
		t.Fatalf("capped upload left staging debris: %v", entries)
	}
}

// The bomb guard is a ratio now, not a fixed size: an archive decompressing
// past 10x its compressed bytes is refused however small it is.
func TestSkillUploadRejectsBombRatio(t *testing.T) {
	userDir := t.TempDir()
	h := newHarness(t, idleRespond, func(c *Config) {
		c.SkillOptions = skills.Options{UserDir: userDir}
	})

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("bomb-skill/SKILL.md")
	_, _ = w.Write([]byte("---\nname: bomb-skill\ndescription: bombs.\n---\nx\n"))
	// 8MB of zeros compresses to a few KB: far past the 10x ratio while the
	// zip itself stays tiny.
	w, _ = zw.Create("bomb-skill/references/zeros.bin")
	if _, err := w.Write(bytes.Repeat([]byte{0}, 8<<20)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	res := postSkillZip(t, h, buf.Bytes())
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "decompresses past") {
		t.Fatalf("bomb: %d (%s), want 400 ratio refusal", res.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(userDir, "bomb-skill")); !os.IsNotExist(err) {
		t.Fatal("refused bomb partially installed")
	}
}
