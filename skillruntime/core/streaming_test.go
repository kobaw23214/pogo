package core

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileBackedArchivePreservesValidation(t *testing.T) {
	for _, tc := range []struct {
		name, entry string
		valid       bool
	}{{"ordinary", "bin/tool", true}, {"escape", "../tool", false}, {"windows-alias", "bin/NUL", false}} {
		t.Run(tc.name, func(t *testing.T) {
			raw := writeZip(t, []zipEnt{{tc.entry, []byte("fixed native tool fixture"), 0555}})
			path := filepath.Join(t.TempDir(), "runtime.zip")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			files, err := extractZIPFile(f, t.TempDir(), &zipBudget{}, "file runtime")
			if !tc.valid {
				if err == nil {
					t.Fatal("unsafe file-backed entry accepted")
				}
				return
			}
			if err != nil || files[tc.entry].Digest != DigestBytes([]byte("fixed native tool fixture")) || !files[tc.entry].Exec {
				t.Fatalf("file-backed extraction lost integrity or executable mode: %#v, %v", files, err)
			}
		})
	}
}

func TestZIP64UncompressedSizeCannotOverflowBudget(t *testing.T) {
	// The byte caps default to unlimited; this guard is about a finite budget
	// being un-overflowable, so the test arms a small one.
	setArchiveLimits(t, 0, 8)
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	header := &zip.FileHeader{Name: "payload", Method: zip.Store, CompressedSize64: ^uint64(0), UncompressedSize64: ^uint64(0)}
	header.SetMode(0444)
	w, err := z.CreateRaw(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte{'a'})
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := extractZIP(data.Bytes(), t.TempDir(), &zipBudget{}, "overflow"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("untrusted ZIP64 size must be rejected during quota preflight: %v", err)
	}
}

func TestNestedArchivesShareExtractionBudget(t *testing.T) {
	setArchiveLimits(t, 0, 10)
	budget := &zipBudget{totalBytes: 5}
	one := writeZip(t, []zipEnt{{"first", []byte("1234"), 0444}})
	two := writeZip(t, []zipEnt{{"second", []byte("12"), 0444}})
	if _, err := extractZIP(one, t.TempDir(), budget, "first payload"); err != nil {
		t.Fatal(err)
	}
	if _, err := extractZIP(two, t.TempDir(), budget, "second payload"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("second payload must not reset cumulative budget: %v", err)
	}
}

// setArchiveLimits arms explicit host caps for one test and restores the
// process-wide values afterwards (0 = unlimited, the default).
func setArchiveLimits(t *testing.T, outer, extracted int64) {
	t.Helper()
	prevOuter, prevExtracted := MaxOuterArchiveBytes, MaxExtractedTotalBytes
	MaxOuterArchiveBytes, MaxExtractedTotalBytes = outer, extracted
	t.Cleanup(func() { MaxOuterArchiveBytes, MaxExtractedTotalBytes = prevOuter, prevExtracted })
}

// The default policy: unlimited byte caps still leave the structural guards
// armed — a lying ZIP64 header is refused by the declared-size check, not the
// budget; a regular small archive extracts with the budget simply counting.
func TestUnlimitedByDefaultStillEnforcesDeclaredSizes(t *testing.T) {
	if MaxOuterArchiveBytes != 0 || MaxExtractedTotalBytes != 0 {
		t.Skipf("host caps armed via env (%d/%d); unlimited-default check does not apply", MaxOuterArchiveBytes, MaxExtractedTotalBytes)
	}
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	header := &zip.FileHeader{Name: "liar", Method: zip.Store, CompressedSize64: 1, UncompressedSize64: 1 << 20}
	header.SetMode(0444)
	w, err := z.CreateRaw(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte{'a'})
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := extractZIP(data.Bytes(), t.TempDir(), &zipBudget{}, "liar"); err == nil {
		t.Fatal("entry expanding past its declared size must be refused even with unlimited byte caps")
	}
	good := writeZip(t, []zipEnt{{"ok", []byte("payload"), 0444}})
	if files, err := extractZIP(good, t.TempDir(), &zipBudget{}, "good"); err != nil || files["ok"].Digest == "" {
		t.Fatalf("regular archive must extract cleanly with unlimited byte caps: %v %v", files, err)
	}
}
