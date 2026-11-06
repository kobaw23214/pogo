package memory

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type NoteType string

const (
	NoteUser      NoteType = "user"
	NoteFeedback  NoteType = "feedback"
	NoteProject   NoteType = "project"
	NoteReference NoteType = "reference"
)

const MaxManagedNoteBytes = 64 << 10

var (
	ErrInvalidNote   = errors.New("invalid memory note")
	ErrSensitiveNote = errors.New("memory note may contain credentials")
	ErrNoteNotFound  = errors.New("memory note not found")
	ErrNoteTooLarge  = errors.New("memory note is too large")
)

type Note struct {
	Path          string    `json:"path"`
	Type          NoteType  `json:"type"`
	Content       string    `json:"content"`
	Modified      time.Time `json:"modified"`
	Created       time.Time `json:"created,omitempty"`
	SourceSession string    `json:"source_session,omitempty"`
	Origin        string    `json:"origin"`
	SizeBytes     int64     `json:"size_bytes"`
}

var noteName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}\.md$`)

func ValidNoteName(path string) bool { return noteName.MatchString(path) }

func ValidNoteType(kind NoteType) bool {
	switch kind {
	case NoteUser, NoteFeedback, NoteProject, NoteReference:
		return true
	default:
		return false
	}
}

var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{16,}`),
	regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?token|auth[_-]?token|secret|password|passwd)\s*[:=]\s*(?:"[^"\r\n]{8,}"|'[^'\r\n]{8,}'|[A-Za-z0-9_./+\-]{12,})`),
	regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s/:@]+:[^\s/@]+@[^\s]+`),
	regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{16,}|ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{16,}|AKIA[A-Z0-9]{16}|AIza[A-Za-z0-9_-]{20,})\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
}

func ContainsSensitive(content string) bool {
	for _, pattern := range sensitivePatterns {
		if pattern.MatchString(content) {
			return true
		}
	}
	return false
}

var noteLocks sync.Map

func LockNoteRoot(root string) func() {
	key, err := filepath.Abs(root)
	if err != nil {
		key = filepath.Clean(root)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(key); resolveErr == nil {
		key = resolved
	}
	value, _ := noteLocks.LoadOrStore(key, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

func ReadNote(root, path string) (Note, error) {
	if !ValidNoteName(path) {
		return Note{}, ErrInvalidNote
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return Note{}, ErrNoteNotFound
	}
	defer handle.Close()
	info, err := handle.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return Note{}, ErrNoteNotFound
	}
	if info.Size() > MaxManagedNoteBytes*2 {
		return Note{}, ErrNoteTooLarge
	}
	file, err := handle.Open(path)
	if err != nil {
		return Note{}, ErrNoteNotFound
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxManagedNoteBytes*2+1))
	if err != nil {
		return Note{}, err
	}
	if len(data) > MaxManagedNoteBytes*2 {
		return Note{}, ErrNoteTooLarge
	}
	note, err := parseNote(path, string(data))
	if err != nil {
		return Note{}, err
	}
	note.SizeBytes = info.Size()
	return note, nil
}

func ListNotes(root string) ([]Note, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return []Note{}, nil
	}
	if err != nil {
		return nil, err
	}
	notes := make([]Note, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || !ValidNoteName(entry.Name()) {
			continue
		}
		note, err := ReadNote(root, entry.Name())
		if err == nil {
			notes = append(notes, note)
		}
	}
	return notes, nil
}

func SaveNote(root string, note Note, rejectSensitive bool) (Note, error) {
	if !ValidNoteName(note.Path) || !ValidNoteType(note.Type) {
		return Note{}, ErrInvalidNote
	}
	note.Content = strings.TrimSpace(stripNoteFrontmatter(note.Content)) + "\n"
	if strings.TrimSpace(note.Content) == "" {
		return Note{}, ErrInvalidNote
	}
	if len(note.Content) > MaxManagedNoteBytes {
		return Note{}, ErrNoteTooLarge
	}
	if rejectSensitive && ContainsSensitive(note.Content) {
		return Note{}, ErrSensitiveNote
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Note{}, err
	}
	if previous, err := ReadNote(root, note.Path); err == nil {
		if note.Created.IsZero() {
			note.Created = previous.Created
		}
		if note.SourceSession == "" {
			note.SourceSession = previous.SourceSession
		}
		if note.Origin == "" {
			note.Origin = previous.Origin
		}
	} else if !errors.Is(err, ErrNoteNotFound) {
		return Note{}, err
	}
	now := time.Now().UTC()
	if note.Modified.IsZero() {
		note.Modified = now
	}
	if note.Created.IsZero() {
		note.Created = note.Modified
	}
	if note.Origin == "" {
		note.Origin = "manual"
	}
	data := []byte(formatNote(note))
	handle, err := os.OpenRoot(root)
	if err != nil {
		return Note{}, err
	}
	defer handle.Close()
	if info, statErr := handle.Lstat(note.Path); statErr == nil && !info.Mode().IsRegular() {
		return Note{}, ErrInvalidNote
	}
	tmp := fmt.Sprintf(".%s.%d.tmp", note.Path, time.Now().UnixNano())
	file, err := handle.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Note{}, err
	}
	ok := false
	defer func() {
		file.Close()
		if !ok {
			_ = handle.Remove(tmp)
		}
	}()
	if _, err = file.Write(data); err != nil {
		return Note{}, err
	}
	if err = file.Sync(); err != nil {
		return Note{}, err
	}
	if err = file.Close(); err != nil {
		return Note{}, err
	}
	if err = handle.Rename(tmp, note.Path); err != nil {
		return Note{}, err
	}
	ok = true
	note.SizeBytes = int64(len(data))
	return note, nil
}

func DeleteNote(root, path string) error {
	if !ValidNoteName(path) {
		return ErrInvalidNote
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return ErrNoteNotFound
	}
	defer handle.Close()
	info, err := handle.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ErrNoteNotFound
	}
	return handle.Remove(path)
}

func parseNote(path, raw string) (Note, error) {
	note := Note{Path: path, Type: NoteProject, Origin: "manual"}
	metadata, body := splitNoteFrontmatter(raw)
	for _, line := range strings.Split(metadata, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "type":
			note.Type = NoteType(value)
		case "modified":
			note.Modified, _ = time.Parse(time.RFC3339, value)
		case "created":
			note.Created, _ = time.Parse(time.RFC3339, value)
		case "source_session":
			note.SourceSession = value
		case "origin":
			note.Origin = value
		}
	}
	if !ValidNoteType(note.Type) {
		return Note{}, ErrInvalidNote
	}
	note.Content = strings.TrimSpace(body) + "\n"
	return note, nil
}

func formatNote(note Note) string {
	return fmt.Sprintf("---\ntype: %s\nmodified: %s\ncreated: %s\nsource_session: %s\norigin: %s\n---\n\n%s",
		note.Type, note.Modified.UTC().Format(time.RFC3339Nano), note.Created.UTC().Format(time.RFC3339), note.SourceSession, note.Origin, note.Content)
}

func splitNoteFrontmatter(raw string) (metadata, body string) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(raw, "---\n") {
		return "", raw
	}
	if end := strings.Index(raw[4:], "\n---\n"); end >= 0 {
		return raw[4 : 4+end], raw[4+end+5:]
	}
	return "", raw
}

func stripNoteFrontmatter(raw string) string {
	_, body := splitNoteFrontmatter(strings.TrimSpace(raw))
	return body
}
