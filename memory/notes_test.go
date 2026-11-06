package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveAndReadNoteMetadata(t *testing.T) {
	root := t.TempDir()
	saved, err := SaveNote(root, Note{Path: "preferences.md", Type: NoteFeedback, Content: "Use pnpm for this project.", SourceSession: "session_1", Origin: "auto-distill"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Created.IsZero() || saved.Modified.IsZero() {
		t.Fatalf("timestamps were not assigned: %+v", saved)
	}
	read, err := ReadNote(root, "preferences.md")
	if err != nil {
		t.Fatal(err)
	}
	if read.Type != NoteFeedback || read.SourceSession != "session_1" || read.Origin != "auto-distill" || read.Content != "Use pnpm for this project.\n" {
		t.Fatalf("metadata did not round trip: %+v", read)
	}
	data, _ := os.ReadFile(filepath.Join(root, "preferences.md"))
	if !strings.Contains(string(data), "type: feedback\n") || !strings.Contains(string(data), "source_session: session_1\n") {
		t.Fatalf("missing frontmatter:\n%s", data)
	}
}

func TestNoteModifiedVersionPreservesNanosecondsAndReadsLegacyTime(t *testing.T) {
	root := t.TempDir()
	version := time.Date(2026, 11, 5, 1, 2, 3, 123456789, time.UTC)
	for _, modified := range []time.Time{version, version.Add(time.Nanosecond)} {
		saved, err := SaveNote(root, Note{Path: "versioned.md", Type: NoteProject, Content: "Versioned fact", Modified: modified}, true)
		if err != nil {
			t.Fatal(err)
		}
		read, err := ReadNote(root, saved.Path)
		if err != nil || !read.Modified.Equal(saved.Modified) || !read.Modified.Equal(modified) {
			t.Fatalf("modified version lost precision: saved %s, read %s, error %v", saved.Modified, read.Modified, err)
		}
	}
	legacy := "---\ntype: project\nmodified: 2026-11-05T01:02:03Z\ncreated: 2026-11-05T01:02:03Z\norigin: manual\n---\n\nLegacy fact\n"
	if err := os.WriteFile(filepath.Join(root, "legacy.md"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	read, err := ReadNote(root, "legacy.md")
	if err != nil || !read.Modified.Equal(version.Truncate(time.Second)) || read.Content != "Legacy fact\n" {
		t.Fatalf("legacy note no longer readable: %+v / %v", read, err)
	}
}

func TestSaveNoteRejectsCredentialsAndUnsafePaths(t *testing.T) {
	root := t.TempDir()
	for _, content := range []string{
		`api_key = "abcdefghijklmnop"`,
		`Authorization: Bearer abcdefghijklmnop`,
		"-----BEGIN RSA " + "PRIVATE KEY-----",
		`postgres://user:password@example.internal/db`,
		`sk-abcdefghijklmnop`,
	} {
		if _, err := SaveNote(root, Note{Path: "secret.md", Type: NoteProject, Content: content}, true); !errors.Is(err, ErrSensitiveNote) {
			t.Errorf("content %q was not rejected: %v", content, err)
		}
	}
	if _, err := SaveNote(root, Note{Path: "safe.md", Type: NoteProject, Content: "Read the API key from POGO_API_KEY; never save its value."}, true); err != nil {
		t.Fatalf("a secret-handling instruction is not a secret: %v", err)
	}
	if _, err := SaveNote(root, Note{Path: "../escape.md", Type: NoteProject, Content: "x"}, false); !errors.Is(err, ErrInvalidNote) {
		t.Fatalf("unsafe path was accepted: %v", err)
	}
}

func TestSaveAndDeleteRejectSymlinks(t *testing.T) {
	root, outside := t.TempDir(), filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveNote(root, Note{Path: "linked.md", Type: NoteProject, Content: "replace"}, false); !errors.Is(err, ErrInvalidNote) {
		t.Fatalf("symlink write was accepted: %v", err)
	}
	if err := DeleteNote(root, "linked.md"); !errors.Is(err, ErrNoteNotFound) {
		t.Fatalf("symlink delete was accepted: %v", err)
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "keep" {
		t.Fatalf("outside file changed: %q", data)
	}
}
