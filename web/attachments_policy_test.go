package web

// The allowlist as one decision, tested at its three layers: normalisation
// (what a saved override means), resolution (override vs default) and the
// gate itself (saveAttachment keeping what the list admits and refusing the
// rest with the list in the message).

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeExts(t *testing.T) {
	got := normalizeExts([]string{"JPG", ".png", "png", ".", " ", "pdf"})
	want := []string{".jpg", ".png", ".pdf"}
	if len(got) != len(want) {
		t.Fatalf("normalizeExts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeExts = %v, want %v", got, want)
		}
	}
	if normalizeExts(nil) != nil || normalizeExts([]string{"", "."}) != nil {
		t.Fatal("an empty selection normalises to nil, not to an empty slice")
	}
}

func TestEffectiveAttachExts(t *testing.T) {
	def := effectiveAttachExts(nil)
	if !def[".png"] || !def[".pdf"] || !def[".docx"] || !def[".xlsx"] {
		t.Error("the default must admit images and the office group")
	}
	if def[".py"] || def[".txt"] || def[".md"] {
		t.Error("the default must not admit the text/code group")
	}

	withText := effectiveAttachExts([]string{".png", ".pdf", ".py", ".sql"})
	if !withText[".py"] || !withText[".sql"] || withText[".docx"] {
		t.Errorf("override = %v, want py+sql in and docx out", sortedExts(withText))
	}

	// An override naming something outside the universe is a hand-edited
	// mistake; the safe reading is the default, not a partial honouring.
	// (.exe is *in* the universe since the binary group — the outsider here
	// has to be something no group claims.)
	bad := effectiveAttachExts([]string{".png", ".xyz123"})
	if bad[".xyz123"] || !bad[".pdf"] {
		t.Errorf("out-of-universe override = %v, want the default set", sortedExts(bad))
	}

	// The binary group is a real group: in the universe, out of the default.
	if !attachUniverse()[".exe"] || def[".exe"] {
		t.Error(".exe must be in the universe (binary group) but not in the default set")
	}
}

// png1x1 is a complete 1×1 PNG: the gate's image branch is decided by
// sniffed content, so the bytes have to actually sniff as image/png.
var png1x1 = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x62, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

func TestSaveAttachmentGatesOnAllowlist(t *testing.T) {
	def := effectiveAttachExts(nil)
	cwd := t.TempDir()

	if _, err := saveAttachment(cwd, "", "报告.docx", "", def, int64(25<<20), bytes.NewReader([]byte("doc"))); err != nil {
		t.Errorf("docx with default list: %v", err)
	}
	// Legacy .doc rides in the office group (default-on) with its own kind,
	// so the run's note steers the model to antiword instead of docx scripts.
	if meta, err := saveAttachment(cwd, "", "旧文档.doc", "", def, int64(25<<20), bytes.NewReader([]byte("\xd0\xcf\x11\xe0garbage"))); err != nil {
		t.Errorf("doc with default list: %v", err)
	} else if meta.Kind != "doc" {
		t.Errorf("doc kind = %q, want doc", meta.Kind)
	}
	_, err := saveAttachment(cwd, "", "notes.md", "", def, int64(25<<20), bytes.NewReader([]byte("# hi")))
	if err == nil || !strings.Contains(err.Error(), "当前允许") {
		t.Errorf("md with default list: got %v, want a refusal naming the allowlist", err)
	}

	textOn := effectiveAttachExts(append(defaultAttachExts(), ".md", ".py"))
	if _, err := saveAttachment(cwd, "", "notes.md", "", textOn, int64(25<<20), bytes.NewReader([]byte("# hi"))); err != nil {
		t.Errorf("md with text group on: %v", err)
	}

	noImages := effectiveAttachExts([]string{".pdf", ".docx"})
	_, err = saveAttachment(cwd, "", "shot.png", "", noImages, int64(25<<20), bytes.NewReader(png1x1))
	if err == nil || !strings.Contains(err.Error(), "当前允许") {
		t.Errorf("png bytes with images off: got %v, want a refusal", err)
	}
	if _, err := saveAttachment(cwd, "", "shot.png", "", def, int64(25<<20), bytes.NewReader(png1x1)); err != nil {
		t.Errorf("png bytes with default list: %v", err)
	}
}

// The binary group: stored as-is with kind binary when allowed, refused with
// the list when not — and never in the default set.
func TestSaveAttachmentBinaryGroup(t *testing.T) {
	cwd := t.TempDir()
	mz := []byte{0x4d, 0x5a, 0x90, 0x00, 1, 2, 3, 4} // MZ header, the sniffable shape of an exe

	_, err := saveAttachment(cwd, "", "sample.exe", "", effectiveAttachExts(nil), int64(25<<20), bytes.NewReader(mz))
	if err == nil || !strings.Contains(err.Error(), "当前允许") {
		t.Errorf("exe with default list: got %v, want a refusal", err)
	}

	binOn := effectiveAttachExts([]string{".exe", ".zip", ".pdf"})
	meta, err := saveAttachment(cwd, "", "sample.exe", "", binOn, int64(25<<20), bytes.NewReader(mz))
	if err != nil {
		t.Fatalf("exe with binary group on: %v", err)
	}
	if meta.Kind != "binary" {
		t.Errorf("kind = %q, want binary", meta.Kind)
	}
	if !strings.HasPrefix(meta.Mime, "application/octet-stream") && meta.Mime != "application/x-dosexec" {
		t.Logf("sniffed mime for the record: %s", meta.Mime)
	}
}

func TestSaveAttachmentMediaGroup(t *testing.T) {
	def := effectiveAttachExts(nil)
	for _, tc := range []struct {
		name, kind, mime string
		data             []byte
	}{
		{"voice.wav", "audio", "audio/wave", []byte("RIFF\x04\x00\x00\x00WAVE")},
		{"movie.mp4", "video", "video/mp4", []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !def[filepath.Ext(tc.name)] {
				t.Fatalf("default allowlist excludes %s", filepath.Ext(tc.name))
			}
			cwd := t.TempDir()
			meta, err := saveAttachment(cwd, "", tc.name, "", def, int64(25<<20), bytes.NewReader(tc.data))
			if err != nil {
				t.Fatalf("default media upload: %v", err)
			}
			if meta.Kind != tc.kind || meta.Mime != tc.mime {
				t.Errorf("media metadata = kind %q, MIME %q; want %q, %q", meta.Kind, meta.Mime, tc.kind, tc.mime)
			}
			if got := fileKindByExt(filepath.Ext(tc.name)); got != tc.kind {
				t.Errorf("fileKindByExt = %q, want %q", got, tc.kind)
			}
			saved, err := os.ReadFile(filepath.Join(cwd, filepath.FromSlash(meta.Path)))
			if err != nil || !bytes.Equal(saved, tc.data) {
				t.Errorf("saved media bytes changed: %v", err)
			}
			if _, err := saveAttachment(cwd, "", tc.name, "", effectiveAttachExts([]string{".pdf"}), int64(25<<20), bytes.NewReader(tc.data)); err == nil {
				t.Error("media must remain disabled by an override that omits its group")
			}
		})
	}
}

func TestEffectiveAttachMaxMB(t *testing.T) {
	if got := effectiveAttachMaxMB(0); got != 25 {
		t.Errorf("no override = %d, want the built-in 25", got)
	}
	if got := effectiveAttachMaxMB(200); got != 200 {
		t.Errorf("in-range override = %d, want 200", got)
	}
	if got := effectiveAttachMaxMB(2); got != 10 {
		t.Errorf("below floor = %d, want clamped to 10", got)
	}
	if got := effectiveAttachMaxMB(9999); got != 1024 {
		t.Errorf("above ceiling = %d, want clamped to 1024", got)
	}
}

// The cap is what the door, the multipart boundary and the body reader all
// use; here it only has to behave as a byte gate in saveAttachment.
func TestSaveAttachmentGatesOnMaxBytes(t *testing.T) {
	def := effectiveAttachExts(nil)
	cwd := t.TempDir()
	data := bytes.Repeat([]byte("a"), 64)

	if _, err := saveAttachment(cwd, "", "small.csv", "", def, 60, bytes.NewReader(data[:60])); err != nil {
		t.Errorf("exactly at the cap: %v", err)
	}
	_, err := saveAttachment(cwd, "", "big.csv", "", def, 60, bytes.NewReader(data))
	if err == nil || !strings.Contains(err.Error(), "上限") {
		t.Errorf("past the cap: got %v, want the cap refusal", err)
	}
}

// Folder uploads: the dir field is client-supplied path material, so the
// interesting behaviour is exactly the boundary — what lands where, and what
// never gets to land at all.
func TestSanitizeRelDir(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{".", ""},
		{"  ", ""},
		{"src", "src"},
		{"src/lib", "src/lib"},
		{"src//lib/", "src/lib"},
		{`src\lib`, "src/lib"}, // the client may speak either separator
		{"项目/素材", "项目/素材"},
		{"/abs", "abs"}, // absolute shapes degrade to a subdir, not an escape
		{"C:\\tmp", "C/tmp"},
	} {
		got, err := sanitizeRelDir(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("sanitizeRelDir(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"..", "src/../..", "a/../../x", "src/..", "./.."} {
		if _, err := sanitizeRelDir(bad); err == nil {
			t.Errorf("sanitizeRelDir(%q) accepted a traversal", bad)
		}
	}
	deep := "a/b/c/d/e/f/g/h/i"
	if _, err := sanitizeRelDir(deep); err == nil {
		t.Error("a tree deeper than the cap must be refused, not walked")
	}
	if _, err := sanitizeRelDir("★"); err == nil {
		t.Error("a segment that sanitises to nothing must be refused, not silently dropped")
	}
}

func TestSaveAttachmentSubdir(t *testing.T) {
	def := effectiveAttachExts(nil)
	cwd := t.TempDir()

	meta, err := saveAttachment(cwd, "project/src", "data.csv", "", def, int64(25<<20), bytes.NewReader([]byte("a,b\n")))
	if err != nil {
		t.Fatalf("nested upload: %v", err)
	}
	if meta.Path != "uploads/project/src/data.csv" {
		t.Errorf("path = %q, want uploads/project/src/data.csv", meta.Path)
	}
	if !isUploadRef(meta.Path) {
		t.Error("a nested upload is an upload ref like any other — the ledger and Delete must both see it")
	}
	if _, err := os.Stat(filepath.Join(cwd, "uploads", "project", "src", "data.csv")); err != nil {
		t.Errorf("the tree must exist on disk: %v", err)
	}

	// Same filename in two trees stays two files: the collision suffix is
	// per directory, so structure does the dedup real folders already do.
	for _, d := range []string{"a", "b"} {
		if _, err := saveAttachment(cwd, d, "data.csv", "", def, int64(25<<20), bytes.NewReader([]byte("z"))); err != nil {
			t.Fatalf("upload under %s: %v", d, err)
		}
	}
	for _, p := range []string{"uploads/a/data.csv", "uploads/b/data.csv"} {
		if _, err := os.Stat(filepath.Join(cwd, filepath.FromSlash(p))); err != nil {
			t.Errorf("%s missing: %v", p, err)
		}
	}
}

// The empty-dir prune: a deleted folder upload leaves no empty husk behind,
// and never eats uploads/ itself or anything outside it.
func TestPruneEmptyUploadDirs(t *testing.T) {
	cwd := t.TempDir()
	root := filepath.Join(cwd, UploadsDir)
	deep := filepath.Join(root, "gone", "tree")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p string) {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(deep, "a.txt"))
	write(filepath.Join(root, "keeps.txt"))

	// The GC order: the file goes first, then the prune reaps what it left.
	if err := os.Remove(filepath.Join(deep, "a.txt")); err != nil {
		t.Fatal(err)
	}
	pruneEmptyUploadDirs(root, deep)
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("uploads/ itself must survive the prune: %v", err)
	}
	if _, err := os.Stat(deep); !os.IsNotExist(err) {
		t.Error("the emptied tree must be gone")
	}
	if _, err := os.Stat(filepath.Join(root, "keeps.txt")); err != nil {
		t.Error("a surviving file is none of the prune's business")
	}
	// A non-empty dir stops the walk, not just errors past it.
	full := filepath.Join(root, "full")
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(full, "b.txt"))
	pruneEmptyUploadDirs(root, full)
	if _, err := os.Stat(filepath.Join(full, "b.txt")); err != nil {
		t.Error("a dir with files in it must not be pruned")
	}
}
