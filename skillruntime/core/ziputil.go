package core

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Host archive policy. Larger native toolchains use the same bounded streaming
// extraction as small packages; these limits are not manifest-controlled.
//
// The two byte caps default to 0 = unlimited: a distribution package carries
// what it carries, and the structural protections below — file count, path
// depth, per-entry declared-size honesty and the compression-ratio guard —
// are what actually stop archive abuse. Operators who want a ceiling back can
// set POGO_SKILL_UPLOAD_MAX_BYTES (outer ZIP) or
// POGO_SKILL_RUNTIME_MAX_EXTRACTED_BYTES (total extracted bytes) to a positive byte
// count; malformed values read as unlimited.
const (
	// MaxArchiveFiles caps the total number of extracted files.
	MaxArchiveFiles = 20000
	// MaxArchiveDepth caps the path depth of archive entries.
	MaxArchiveDepth = 32
	// maxCompressionRatio rejects likely zip bombs: an entry expanding more
	// than 1000x while being larger than 16 MiB uncompressed is refused.
	maxCompressionRatio = 1000
	ratioCheckMinBytes  = 16 << 20
)

var (
	// MaxOuterArchiveBytes caps the outer distribution ZIP; 0 = unlimited.
	MaxOuterArchiveBytes = archiveLimitFromEnv("POGO_SKILL_UPLOAD_MAX_BYTES")
	// MaxExtractedTotalBytes caps the total extracted bytes across the outer
	// archive and all payload archives (nested ZIP bytes included);
	// 0 = unlimited.
	MaxExtractedTotalBytes = archiveLimitFromEnv("POGO_SKILL_RUNTIME_MAX_EXTRACTED_BYTES")
)

func archiveLimitFromEnv(name string) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// zipBudget tracks cumulative extraction quotas across nested archives.
type zipBudget struct {
	totalBytes int64
	totalFiles int
}

// normalizeZipName validates and normalizes one ZIP entry name. It returns
// the clean relative path (forward slashes) or an error.
func normalizeZipName(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty entry name")
	}
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("entry name %q is not valid UTF-8", name)
	}
	if strings.Contains(name, "\\") {
		return "", fmt.Errorf("entry name %q contains a backslash", name)
	}
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("entry name %q is absolute", name)
	}
	if len(name) >= 2 && name[1] == ':' {
		return "", fmt.Errorf("entry name %q contains a drive letter", name)
	}
	name = strings.TrimPrefix(name, "./")
	name = strings.TrimSuffix(name, "/") // directory marker
	if name == "" {
		return "", fmt.Errorf("empty entry name")
	}
	parts := strings.Split(name, "/")
	if len(parts) > MaxArchiveDepth {
		return "", fmt.Errorf("entry name %q exceeds depth %d", name, MaxArchiveDepth)
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", fmt.Errorf("entry name %q contains an empty or dot segment", name)
		}
		if strings.ContainsAny(p, "<>:\"|?*\x00") || strings.TrimRight(p, ". ") != p {
			return "", fmt.Errorf("entry name %q is not a portable regular-file path", name)
		}
		for _, r := range p {
			if r < 32 {
				return "", fmt.Errorf("entry name %q contains a control character", name)
			}
		}
		stem := strings.ToUpper(strings.SplitN(p, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
			return "", fmt.Errorf("entry name %q is a reserved device path", name)
		}
	}
	return strings.Join(parts, "/"), nil
}

// extractZIP safely extracts a ZIP archive into dest and returns a map of
// extracted file relative paths to their content digests. It rejects
// absolute/escaping paths, non-UTF-8 names, duplicate normalized names,
// case-insensitive collisions, symlinks and special files, quota bypass and
// likely zip bombs. The executable bit of entry modes is preserved.
func extractZIP(data []byte, dest string, budget *zipBudget, label string) (map[string]extractedFile, error) {
	return extractZIPReader(bytes.NewReader(data), int64(len(data)), dest, budget, label)
}

func extractZIPFile(file *os.File, dest string, budget *zipBudget, label string) (map[string]extractedFile, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || (MaxOuterArchiveBytes > 0 && info.Size() > MaxOuterArchiveBytes) {
		return nil, fmt.Errorf("%s: payload is not a regular archive within the host size limit", label)
	}
	return extractZIPReader(file, info.Size(), dest, budget, label)
}

func extractZIPReader(source io.ReaderAt, size int64, dest string, budget *zipBudget, label string) (map[string]extractedFile, error) {
	zr, err := zip.NewReader(source, size)
	if err != nil {
		return nil, fmt.Errorf("%s: not a readable ZIP archive: %v", label, err)
	}

	seen := map[string]bool{}     // exact normalized names
	seenFold := map[string]bool{} // case-folded names
	digests := map[string]extractedFile{}

	// Preflight on declared headers before writing anything.
	type entry struct {
		f    *zip.File
		name string
		dir  bool
	}
	var entries []entry
	for _, f := range zr.File {
		name, err := normalizeZipName(f.Name)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", label, err)
		}
		isDir := f.FileInfo().IsDir() || strings.HasSuffix(f.Name, "/")
		mode := f.FileInfo().Mode()
		if mode&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s: entry %q is a symlink; links are forbidden", label, name)
		}
		if !isDir && !mode.IsRegular() {
			return nil, fmt.Errorf("%s: entry %q is a special file (mode %s); forbidden", label, name, mode)
		}
		if f.Method != zip.Store && f.Method != zip.Deflate {
			return nil, fmt.Errorf("%s: entry %q uses unsupported compression method %d", label, name, f.Method)
		}
		if seen[name] {
			return nil, fmt.Errorf("%s: duplicate entry %q", label, name)
		}
		seen[name] = true
		folded := strings.ToLower(name)
		if seenFold[folded] {
			return nil, fmt.Errorf("%s: entry %q collides with another entry under case folding", label, name)
		}
		seenFold[folded] = true
		if !isDir {
			if f.UncompressedSize64 > ratioCheckMinBytes &&
				f.CompressedSize64 > 0 &&
				f.UncompressedSize64/f.CompressedSize64 > maxCompressionRatio {
				return nil, fmt.Errorf("%s: entry %q has a suspicious compression ratio (%d -> %d bytes)",
					label, name, f.CompressedSize64, f.UncompressedSize64)
			}
			budget.totalFiles++
			if budget.totalFiles > MaxArchiveFiles {
				return nil, fmt.Errorf("%s: more than %d files in total", label, MaxArchiveFiles)
			}
			if MaxExtractedTotalBytes > 0 &&
				(budget.totalBytes > MaxExtractedTotalBytes || f.UncompressedSize64 > uint64(MaxExtractedTotalBytes-budget.totalBytes)) {
				return nil, fmt.Errorf("%s: extracted data exceeds %d bytes in total", label, MaxExtractedTotalBytes)
			}
			budget.totalBytes += int64(f.UncompressedSize64)
		}
		entries = append(entries, entry{f: f, name: name, dir: isDir})
	}

	// Extraction pass.
	for _, e := range entries {
		target := filepath.Join(dest, filepath.FromSlash(e.name))
		absDest, err := filepath.Abs(dest)
		if err != nil {
			return nil, err
		}
		absTarget, err := filepath.Abs(target)
		if err != nil {
			return nil, err
		}
		if absTarget != absDest && !strings.HasPrefix(absTarget, absDest+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s: entry %q escapes the extraction directory", label, e.name)
		}
		mode := e.f.FileInfo().Mode()
		if e.dir {
			if err := os.MkdirAll(absTarget, 0o755); err != nil {
				return nil, fmt.Errorf("%s: mkdir %q: %v", label, e.name, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(absTarget), 0o755); err != nil {
			return nil, fmt.Errorf("%s: mkdir for %q: %v", label, e.name, err)
		}
		perm := os.FileMode(0o644)
		if mode&0o100 != 0 {
			perm = 0o755 // preserve the executable bit
		}
		rc, err := e.f.Open()
		if err != nil {
			return nil, fmt.Errorf("%s: open entry %q: %v", label, e.name, err)
		}
		// Limit reads to the declared uncompressed size + 1 to catch lies.
		limited := io.LimitReader(rc, int64(e.f.UncompressedSize64)+1)
		tmp := absTarget + ".tmp-pogo"
		w, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if err != nil {
			rc.Close()
			return nil, fmt.Errorf("%s: create %q: %v", label, e.name, err)
		}
		h := newDigestWriter(w)
		n, copyErr := io.Copy(h, limited)
		closeErr := w.Close()
		rc.Close()
		if copyErr != nil {
			os.Remove(tmp)
			return nil, fmt.Errorf("%s: extract %q: %v", label, e.name, copyErr)
		}
		if closeErr != nil {
			os.Remove(tmp)
			return nil, fmt.Errorf("%s: write %q: %v", label, e.name, closeErr)
		}
		if n != int64(e.f.UncompressedSize64) {
			os.Remove(tmp)
			return nil, fmt.Errorf("%s: entry %q expanded to %d bytes, header declared %d",
				label, e.name, n, e.f.UncompressedSize64)
		}
		if err := os.Rename(tmp, absTarget); err != nil {
			os.Remove(tmp)
			return nil, fmt.Errorf("%s: publish %q: %v", label, e.name, err)
		}
		digests[e.name] = extractedFile{Digest: h.sum(), Exec: mode&0o100 != 0}
	}
	return digests, nil
}

// extractedFile records the verified digest and the ZIP-declared executable
// bit of one extracted file. The bit comes from the archive header so it is
// meaningful even on filesystems (Windows) that cannot persist it.
type extractedFile struct {
	Digest string
	Exec   bool
}
