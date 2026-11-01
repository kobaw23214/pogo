package skills

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDocumentPathBoundary(t *testing.T) {
	for _, value := range []string{"/etc/passwd", "../other/SKILL.md", "references/../../outside", "references/../SKILL.md", "C:/secret", `references\secret`, "%2e%2e/secret", "references/%252e%252e/file", "file.md?x=1", "file.md#x", "file\x00.md", ".", "file\n.md"} {
		if _, err := DocumentPath(value); !errors.Is(err, ErrDocumentPath) {
			t.Errorf("DocumentPath(%q) = %v, want path rejection", value, err)
		}
	}
	for value, want := range map[string]string{"": "SKILL.md", "./references/说明.md": "references/说明.md", "references/sql-reuse/workflow.md": "references/sql-reuse/workflow.md"} {
		if got, err := DocumentPath(value); err != nil || got != want {
			t.Errorf("DocumentPath(%q) = %q, %v", value, got, err)
		}
	}
}

func TestReadDocumentConfinesFilesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	write := func(name, value string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(value), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "SKILL.md"), "# Instructions")
	write(filepath.Join(root, "references", "flow.md"), "# Flow\n参考资料")
	write(filepath.Join(outside, "secret.md"), "outside secret")
	for link, target := range map[string]string{"escape.md": filepath.Join(outside, "secret.md"), "directory": outside, "inside.md": "references/flow.md"} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	sk := Skill{Name: "demo", Dir: root, Path: filepath.Join(root, "SKILL.md")}
	for _, relative := range []string{"", "references/flow.md", "inside.md"} {
		data, truncated, err := ReadDocument(sk, relative)
		if err != nil || truncated || !bytes.HasPrefix(data, []byte("# ")) {
			t.Errorf("read %q: %q %v %v", relative, data, truncated, err)
		}
	}
	for _, relative := range []string{"escape.md", "directory/secret.md", "references", "missing.md"} {
		if data, _, err := ReadDocument(sk, relative); !errors.Is(err, ErrDocumentUnavailable) || len(data) != 0 {
			t.Errorf("read %q leaked %q (%v)", relative, data, err)
		}
	}
}

func TestReadDocumentTextAndBoundedUTF8(t *testing.T) {
	root := t.TempDir()
	sk := Skill{Dir: root}
	files := map[string][]byte{
		"empty.txt": {}, "unicode.md": []byte("# 中文文档\n正文"),
		"binary.pdf": []byte("%PDF-1.7\nASCII but not a text preview"), "binary.zip": []byte("PK\x03\x04\x00data"),
		"invalid.txt": {0xff, 0xfe}, "control.txt": []byte("text\x1bescape"),
		"large.md":          []byte(strings.Repeat("a", DocumentPreviewLimit-1) + "中文"),
		"invalid-large.txt": append(bytes.Repeat([]byte("a"), DocumentPreviewLimit-1), 0xff, 'a'),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"binary.pdf", "binary.zip", "invalid.txt", "control.txt", "invalid-large.txt"} {
		if _, _, err := ReadDocument(sk, name); !errors.Is(err, ErrDocumentNotText) {
			t.Errorf("%s: %v, want unsupported text", name, err)
		}
	}
	for _, name := range []string{"empty.txt", "unicode.md", "large.md"} {
		data, truncated, err := ReadDocument(sk, name)
		if err != nil || !utf8.Valid(data) || len(data) > DocumentPreviewLimit || truncated != (name == "large.md") {
			t.Errorf("%s: len=%d truncated=%v err=%v", name, len(data), truncated, err)
		}
	}
}

func TestHostBoundDocumentNeverFallsBackToPrivateDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "secret.md"), []byte("private secret"), 0644); err != nil {
		t.Fatal(err)
	}
	sk := Skill{Dir: root, ReadContent: func() ([]byte, error) { return []byte("safe instructions"), nil }}
	if data, _, err := ReadDocument(sk, ""); err != nil || string(data) != "safe instructions" {
		t.Fatal(string(data), err)
	}
	if data, _, err := ReadDocument(sk, "secret.md"); !errors.Is(err, ErrDocumentUnavailable) || len(data) > 0 {
		t.Fatal("private fallback", string(data), err)
	}
	sk.ReadDocument = func(relative string) ([]byte, bool, error) {
		if relative == "references/flow.md" {
			return []byte("bounded reference"), false, nil
		}
		return nil, false, ErrDocumentUnavailable
	}
	if data, _, err := ReadDocument(sk, "references/flow.md"); err != nil || string(data) != "bounded reference" {
		t.Fatal(string(data), err)
	}
}
