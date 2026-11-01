package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillFilesLazyMetadataAndSafePreview(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	write := func(relative, text string) {
		t.Helper()
		name := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("SKILL.md", "# Main")
	write("references/sql/flow.md", "# SQL reference")
	write("scripts/run.sh", "#!/bin/sh\ntouch NEVER_EXECUTED\n")
	write("tools.json", "{\"tools\":[]}")
	write("image.png", "\x89PNG\r\n\x1a\n\x00binary")
	write("odd%name.md", "# No ambiguous encoded paths")
	if err := os.WriteFile(filepath.Join(outside, "private.md"), []byte("private data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("references", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	sk := Skill{Dir: root, Path: filepath.Join(root, "SKILL.md")}
	listed, err := ListFiles(sk, "")
	if err != nil || listed.RelativePath != "" || listed.Truncated {
		t.Fatalf("root: %+v %v", listed, err)
	}
	entries := map[string]FileEntry{}
	for _, entry := range listed.Entries {
		entries[entry.Path] = entry
		if strings.Contains(entry.Path, root) || strings.Contains(entry.Path, outside) {
			t.Fatal("host path exposed")
		}
		if strings.Contains(entry.Path, "/") {
			t.Fatalf("root enumeration was not lazy: %q", entry.Path)
		}
	}
	if entries["SKILL.md"].Kind != "file" || !entries["SKILL.md"].Previewable || entries["SKILL.md"].Language != "markdown" {
		t.Fatal(entries["SKILL.md"])
	}
	if entries["references"].Kind != "directory" || !entries["references"].Expandable {
		t.Fatal(entries["references"])
	}
	if !entries["tools.json"].Previewable || entries["image.png"].Previewable || entries["image.png"].Reason == "" {
		t.Fatal("text/binary classification", entries)
	}
	if entries["odd%name.md"].Previewable || entries["odd%name.md"].Reason == "" {
		t.Fatal("ambiguous name previewed")
	}
	for _, name := range []string{"escape", "alias"} {
		if entries[name].Kind != "symlink" || entries[name].Expandable || entries[name].Previewable {
			t.Fatalf("link was followed: %+v", entries[name])
		}
		if _, err := ListFiles(sk, name); !errors.Is(err, ErrDocumentUnavailable) {
			t.Errorf("link %s: %v", name, err)
		}
	}
	listed, err = ListFiles(sk, "references/sql")
	if err != nil || len(listed.Entries) != 1 || listed.Entries[0].Path != "references/sql/flow.md" || !listed.Entries[0].Previewable {
		t.Fatalf("nested lazy enumeration: %+v %v", listed, err)
	}
	if _, err := os.Stat(filepath.Join(root, "NEVER_EXECUTED")); !os.IsNotExist(err) {
		t.Fatal("preview executed uploaded content")
	}
	for _, relative := range []string{"../outside", "/etc", "%2e%2e", `references\sql`, "references/../scripts"} {
		if _, err := ListFiles(sk, relative); !errors.Is(err, ErrDocumentPath) {
			t.Errorf("%q: %v", relative, err)
		}
	}
	if _, err := ListFiles(Skill{Dir: root, ReadContent: func() ([]byte, error) { return nil, nil }}, ""); !errors.Is(err, ErrDocumentUnavailable) {
		t.Fatal("private host reader fell back to Dir", err)
	}
}

func TestSkillFilesEntryAndDepthLimits(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < FileListMaxEntries+3; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%04d.md", i)), []byte("# text"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := ListDirectoryFiles(root, "", nil)
	if err != nil || !listed.Truncated || len(listed.Entries) != FileListMaxEntries || listed.Limits.MaxEntries != FileListMaxEntries {
		t.Fatalf("entry limit: %d %+v %v", len(listed.Entries), listed.Limits, err)
	}
	deep := t.TempDir()
	relative := strings.TrimSuffix(strings.Repeat("dir/", FileListMaxDepth), "/")
	if err := os.MkdirAll(filepath.Join(deep, filepath.FromSlash(relative)), 0755); err != nil {
		t.Fatal(err)
	}
	parent := strings.TrimSuffix(strings.Repeat("dir/", FileListMaxDepth-1), "/")
	listed, err = ListDirectoryFiles(deep, parent, nil)
	if err != nil || len(listed.Entries) != 1 || listed.Entries[0].Expandable || listed.Entries[0].Reason == "" {
		t.Fatalf("deep listing: %+v %v", listed, err)
	}
	if _, err := ListDirectoryFiles(deep, relative, nil); !errors.Is(err, ErrDocumentPath) {
		t.Fatal("depth limit not enforced", err)
	}
}
