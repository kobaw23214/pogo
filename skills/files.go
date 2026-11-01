package skills

import (
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"
)

const (
	FileListMaxEntries = 1000
	FileListMaxDepth   = 16
)

// FileEntry contains package-relative metadata only. Links are never followed
// while enumerating, and their targets (which may be host paths) are not exposed.
type FileEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	SizeBytes   int64  `json:"size_bytes"`
	Previewable bool   `json:"previewable"`
	Expandable  bool   `json:"expandable"`
	Language    string `json:"language,omitempty"`
	MediaType   string `json:"media_type,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type FileListLimits struct {
	MaxEntries int `json:"max_entries"`
	MaxDepth   int `json:"max_depth"`
}

type FileList struct {
	RelativePath string         `json:"relative_path"`
	Entries      []FileEntry    `json:"entries"`
	Truncated    bool           `json:"truncated"`
	Limits       FileListLimits `json:"limits"`
}

// ListFiles lists one directory of an ordinary Skill. Host-bound Skill sources
// must use their own package reader instead of falling back to their private Dir.
func ListFiles(skill Skill, relative string) (FileList, error) {
	if skill.ReadContent != nil || skill.ReadDocument != nil {
		return FileList{}, ErrDocumentUnavailable
	}
	root := skill.Dir
	if root == "" {
		if skill.Path == "" {
			return FileList{}, ErrDocumentUnavailable
		}
		root = filepath.Dir(skill.Path)
	}
	return ListDirectoryFiles(root, relative, nil)
}

// ListDirectoryFiles is a bounded, lazy administrator preview of a package
// directory. canPreview may narrow readable metadata for private runtime
// packages; it does not change Agent tools or expose a host filesystem root.
func ListDirectoryFiles(rootDir, relative string, canPreview func(string) bool) (FileList, error) {
	result := FileList{Entries: []FileEntry{}, Limits: FileListLimits{FileListMaxEntries, FileListMaxDepth}}
	if relative != "" {
		clean, err := DocumentPath(relative)
		if err != nil || strings.Count(clean, "/")+1 >= FileListMaxDepth {
			return result, ErrDocumentPath
		}
		relative = clean
	}
	result.RelativePath = relative
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return result, ErrDocumentUnavailable
	}
	defer root.Close()
	// Reject each requested link component rather than showing the contents of
	// an alias directory. os.Root keeps the subsequent open confined during races.
	if relative != "" {
		var current string
		for _, component := range strings.Split(relative, "/") {
			current = path.Join(current, component)
			info, err := root.Lstat(filepath.FromSlash(current))
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return result, ErrDocumentUnavailable
			}
		}
	}
	directory := "."
	if relative != "" {
		directory = filepath.FromSlash(relative)
	}
	f, err := root.OpenFile(directory, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return result, ErrDocumentUnavailable
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.IsDir() {
		return result, ErrDocumentUnavailable
	}
	children, err := f.ReadDir(FileListMaxEntries + 1)
	if err != nil && err != io.EOF {
		return result, ErrDocumentUnavailable
	}
	if len(children) > FileListMaxEntries {
		children, result.Truncated = children[:FileListMaxEntries], true
	}
	for _, child := range children {
		filename := path.Join(relative, child.Name())
		entry := FileEntry{Name: child.Name(), Path: filename, Kind: "other"}
		info, err := root.Lstat(filepath.FromSlash(filename))
		if err != nil {
			entry.Reason = "此条目已不存在或不可读取"
		} else {
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				entry.Kind, entry.Reason = "symlink", "符号链接不支持浏览或预览"
			case info.IsDir():
				entry.Kind = "directory"
				entry.Expandable = strings.Count(filename, "/")+1 < FileListMaxDepth
				if !entry.Expandable {
					entry.Reason = "目录深度超过预览上限"
				}
			case info.Mode().IsRegular():
				entry.Kind, entry.SizeBytes = "file", info.Size()
				entry.Language, entry.MediaType = DocumentFormat(filename)
				if media := mime.TypeByExtension(path.Ext(filename)); media != "" {
					entry.MediaType = media
				}
				if canPreview != nil && !canPreview(filename) {
					entry.Reason = "运行包仅支持预览 SKILL.md 和 references/ 文本文档"
				} else {
					entry.Previewable, entry.Reason, entry.MediaType = probePackageText(root, filename, entry.MediaType)
					if entry.Previewable && entry.SizeBytes > DocumentPreviewLimit {
						entry.Reason = "预览最多显示 256 KiB"
					}
				}
			default:
				entry.Reason = "此类型不支持预览"
			}
		}
		if _, err := DocumentPath(filename); err != nil {
			entry.Previewable, entry.Expandable = false, false
			entry.Reason = "此条目名称不支持安全预览"
		}
		result.Entries = append(result.Entries, entry)
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		a, b := result.Entries[i], result.Entries[j]
		if (a.Kind == "directory") != (b.Kind == "directory") {
			return a.Kind == "directory"
		}
		return a.Name < b.Name
	})
	return result, nil
}

func probePackageText(root *os.Root, filename, mediaType string) (bool, string, string) {
	if _, err := DocumentPath(filename); err != nil {
		return false, "此条目名称不支持安全预览", mediaType
	}
	f, err := root.OpenFile(filepath.FromSlash(filename), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false, "此文件不可读取", mediaType
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return false, "此文件不可读取", mediaType
	}
	data, err := io.ReadAll(io.LimitReader(f, 512))
	if err != nil {
		return false, "此文件不可读取", mediaType
	}
	// Sniffing is limited to one small block, even for multi-GB runtime archives.
	// Avoid rejecting text merely because that block ends inside a UTF-8 rune.
	if len(data) == 512 && !utf8.Valid(data) {
		for n := 1; n < utf8.UTFMax; n++ {
			prefix, tail := data[:len(data)-n], data[len(data)-n:]
			if utf8.Valid(prefix) && !utf8.FullRune(tail) {
				data = prefix
				break
			}
		}
	}
	if !isDocumentText(data) {
		return false, "此文件不是可预览的 UTF-8 文本文档", http.DetectContentType(data)
	}
	return true, "", mediaType
}
