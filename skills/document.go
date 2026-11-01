package skills

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

const DocumentPreviewLimit = 256 << 10

var (
	ErrDocumentPath        = errors.New("Skill 文档路径无效，只能读取当前 Skill 包内的相对路径")
	ErrDocumentUnavailable = errors.New("Skill 文档不存在或不可读取")
	ErrDocumentNotText     = errors.New("此文件不是可预览的 UTF-8 文本文档")
)

// DocumentPath accepts a decoded package-root-relative URL path. It never
// decodes again: percent escapes, absolute paths and parent traversal fail
// closed rather than acquiring a second interpretation further down the stack.
func DocumentPath(relative string) (string, error) {
	if relative == "" {
		return "SKILL.md", nil
	}
	if strings.ContainsAny(relative, "\\:%?#") || strings.HasPrefix(relative, "/") || strings.ContainsFunc(relative, unicode.IsControl) {
		return "", ErrDocumentPath
	}
	for _, component := range strings.Split(relative, "/") {
		if component == ".." {
			return "", ErrDocumentPath
		}
	}
	clean := path.Clean(relative)
	if clean == "." || clean == ".." || !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", ErrDocumentPath
	}
	return clean, nil
}

// ReadDocument previews a text file without expanding the Skill's trust root.
// os.Root also confines symlink resolution when a file changes during a read.
// Host-bound packages supply their own reader; their opaque Dir is never opened.
func ReadDocument(s Skill, relative string) ([]byte, bool, error) {
	relative, err := DocumentPath(relative)
	if err != nil {
		return nil, false, err
	}
	var data []byte
	var truncated bool
	switch {
	case s.ReadDocument != nil:
		data, truncated, err = s.ReadDocument(relative)
	case s.ReadContent != nil:
		if relative != "SKILL.md" {
			return nil, false, ErrDocumentUnavailable
		}
		data, err = s.ReadContent()
	default:
		rootDir := s.Dir
		if rootDir == "" {
			if s.Path == "" {
				return nil, false, ErrDocumentUnavailable
			}
			rootDir = filepath.Dir(s.Path)
		}
		filename := filepath.FromSlash(relative)
		// Explicit --skill files may have a name other than SKILL.md. The
		// default remains the loader's vetted instruction file in that case.
		if relative == "SKILL.md" && s.Path != "" {
			entry, relErr := filepath.Rel(rootDir, s.Path)
			if relErr != nil || !filepath.IsLocal(entry) {
				return nil, false, ErrDocumentUnavailable
			}
			filename = entry
		}
		root, openErr := os.OpenRoot(rootDir)
		if openErr != nil {
			return nil, false, ErrDocumentUnavailable
		}
		defer root.Close()
		// Check before opening to avoid blocking on devices/FIFOs. os.Root
		// confines the following open even if a symlink is replaced meanwhile.
		info, statErr := root.Stat(filename)
		if statErr != nil || !info.Mode().IsRegular() {
			return nil, false, ErrDocumentUnavailable
		}
		f, openErr := root.OpenFile(filename, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if openErr != nil {
			return nil, false, ErrDocumentUnavailable
		}
		defer f.Close()
		info, statErr = f.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			return nil, false, ErrDocumentUnavailable
		}
		data, err = io.ReadAll(io.LimitReader(f, DocumentPreviewLimit+1))
	}
	if err != nil {
		if errors.Is(err, ErrDocumentPath) || errors.Is(err, ErrDocumentNotText) {
			return nil, false, err
		}
		return nil, false, ErrDocumentUnavailable
	}
	if len(data) > DocumentPreviewLimit {
		data, truncated = data[:DocumentPreviewLimit], true
	}
	// A bounded read can end in the middle of one UTF-8 rune. Remove only
	// this incomplete tail, while rejecting invalid bytes in the actual body.
	if truncated && !utf8.Valid(data) {
		for n := 1; n < utf8.UTFMax && n <= len(data); n++ {
			prefix, tail := data[:len(data)-n], data[len(data)-n:]
			if utf8.Valid(prefix) && !utf8.FullRune(tail) {
				data = prefix
				break
			}
		}
	}
	if !isDocumentText(data) {
		return nil, false, ErrDocumentNotText
	}
	return data, truncated, nil
}

func isDocumentText(data []byte) bool {
	if !utf8.Valid(data) || bytes.ContainsFunc(data, func(r rune) bool {
		return r < 0x20 && r != '\t' && r != '\n' && r != '\r' && r != '\f'
	}) {
		return false
	}
	media := http.DetectContentType(data)
	return strings.HasPrefix(media, "text/") || strings.HasPrefix(media, "application/xml") || len(data) == 0
}

// DocumentFormat describes source text, never an executable response type.
func DocumentFormat(relative string) (language, mediaType string) {
	ext := strings.ToLower(path.Ext(relative))
	if ext == ".md" || ext == ".markdown" {
		return "markdown", "text/markdown"
	}
	languages := map[string]string{
		".json": "json", ".yaml": "yaml", ".yml": "yaml", ".toml": "toml", ".xml": "xml", ".svg": "xml",
		".html": "html", ".htm": "html", ".css": "css", ".js": "javascript", ".mjs": "javascript", ".ts": "typescript",
		".py": "python", ".go": "go", ".rs": "rust", ".java": "java", ".sql": "sql", ".sh": "bash", ".bash": "bash", ".properties": "properties",
	}
	if language = languages[ext]; language == "" {
		language = "text"
	}
	return language, "text/plain"
}
