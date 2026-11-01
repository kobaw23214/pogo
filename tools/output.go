package tools

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// MaxCommandOutputBytes bounds one command's output, including its spill file.
// Crossing the limit cancels the command; a partial file is never called full.
const MaxCommandOutputBytes int64 = 64 << 20

var ErrCommandOutputLimit = errors.New("command output exceeded the 64MB limit; command stopped")

// outputCapture spools while the command runs and keeps only a bounded tail.
// Disk failures degrade to the tail, without rebuilding a full in-memory copy.
type outputCapture struct {
	mu              sync.Mutex
	dir, tool       string
	limit           int64
	cancel          func()
	total           int64
	newlines        int
	last            byte
	tail            []byte
	tailAtLineStart bool
	file            *os.File
	sum             hash.Hash
	diskErr         error
	limitErr        error
	finished        bool
	kept            Kept
	root            *os.Root // only scoped commands; never reopen through a path
	rootTemp        string
}

func newOutputCapture(dir, tool string, cancel func()) *outputCapture {
	return &outputCapture{dir: dir, tool: tool, cancel: cancel, limit: MaxCommandOutputBytes,
		tail: make([]byte, 0, MaxBytes+1), tailAtLineStart: true, sum: sha256.New()}
}

// A scoped shell can race the host-side collector by replacing directories
// with symlinks. Anchor the collector at the session root before execution and
// perform every create/rename/remove through that root, not its display path.
func newScopedOutputCapture(cwd, tool string, cancel func()) (*outputCapture, error) {
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return nil, errors.New("cannot open session output root")
	}
	out := newOutputCapture(cwd, tool, cancel)
	out.root = root
	return out, nil
}

func (s *outputCapture) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return 0, os.ErrClosed
	}
	if s.limitErr != nil {
		return 0, s.limitErr
	}
	n := len(p)
	if int64(n) > s.limit-s.total {
		n = int(s.limit - s.total)
	}
	part := p[:n]
	if len(part) > 0 {
		if s.file == nil && s.diskErr == nil {
			s.open()
		}
		if s.file != nil && s.diskErr == nil {
			written, err := s.file.Write(part)
			if err == nil && written != len(part) {
				err = io.ErrShortWrite
			}
			s.diskErr = err
		}
		_, _ = s.sum.Write(part)
		s.newlines += bytes.Count(part, []byte{'\n'})
		s.last = part[len(part)-1]
		s.total += int64(len(part))
		s.appendTail(part)
	}
	if n != len(p) {
		s.limitErr = ErrCommandOutputLimit
		if s.cancel != nil {
			s.cancel()
		}
	}
	return n, s.limitErr
}

func (s *outputCapture) open() {
	if s.root != nil {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			s.diskErr = err
			return
		}
		s.rootTemp = ".partial-" + hex.EncodeToString(nonce[:])
		s.file, s.diskErr = s.root.OpenFile(s.rootTemp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		return
	}
	dir := s.dir
	if strings.TrimSpace(dir) == "" {
		dir = DefaultSpillDir()
	}
	abs, err := filepath.Abs(dir)
	if err == nil {
		err = os.MkdirAll(abs, 0o700)
	}
	if err == nil {
		s.file, err = os.CreateTemp(abs, ".partial-*")
	}
	s.diskErr = err
}

func (s *outputCapture) appendTail(p []byte) {
	const size = MaxBytes + 1
	if len(p) >= size {
		drop := len(p) - size
		if drop > 0 {
			s.tailAtLineStart = p[drop-1] == '\n'
		} else if len(s.tail) > 0 {
			s.tailAtLineStart = s.tail[len(s.tail)-1] == '\n'
		}
		s.tail = s.tail[:size]
		copy(s.tail, p[drop:])
		return
	}
	if drop := len(s.tail) + len(p) - size; drop > 0 {
		s.tailAtLineStart = s.tail[drop-1] == '\n'
		copy(s.tail, s.tail[drop:])
		s.tail = s.tail[:len(s.tail)-drop]
	}
	s.tail = append(s.tail, p...)
}

// finish is idempotent, and must run after all output writers have stopped.
func (s *outputCapture) finish() Kept {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return s.kept
	}
	s.finished = true
	if s.root != nil {
		defer s.root.Close()
	}
	raw := s.tail
	dropped := s.total > int64(len(raw))
	if dropped && !s.tailAtLineStart {
		// A cut first line is not a complete line of evidence. Keep it only
		// when it is the sole (oversized) last line, as TruncateTail does.
		if end := bytes.IndexByte(raw, '\n'); end >= 0 && end < len(raw)-1 {
			raw = raw[end+1:]
		}
	}
	for len(raw) > 0 && !isRuneStart(raw[0]) {
		raw = raw[1:]
	}
	tr := TruncateTail(string(raw))
	tr.TotalLines = s.newlines
	if s.total > 0 && s.last != '\n' {
		tr.TotalLines++
	}
	if dropped {
		// TruncateTail strips the final line separator on truncated output.
		// The retained suffix may itself fit, but the original output did not.
		if !tr.Truncated {
			tr.Content = strings.TrimSuffix(tr.Content, "\n")
		}
		tr.Truncated = true
		tr.By = "bytes"
	}
	s.kept = Kept{Truncation: tr}
	if s.file != nil {
		tmp := s.file.Name()
		if tr.Truncated && s.diskErr == nil && s.limitErr == nil {
			s.diskErr = s.file.Sync()
		}
		if err := s.file.Close(); s.diskErr == nil {
			s.diskErr = err
		}
		if tr.Truncated && s.diskErr == nil && s.limitErr == nil {
			digest := s.sum.Sum(nil)
			final := filepath.Join(filepath.Dir(tmp), fmt.Sprintf("%s-%x.log", spillPrefix(s.tool), digest[:6]))
			var err error
			if s.root != nil {
				name := fmt.Sprintf("%s-%x.log", spillPrefix(s.tool), digest[:6])
				err = s.root.Rename(s.rootTemp, name)
				final = filepath.Join(s.dir, name)
			} else {
				err = os.Rename(tmp, final)
			}
			if err == nil {
				s.kept.Path = final
			}
		}
		if s.kept.Path == "" {
			if s.root != nil {
				_ = s.root.Remove(s.rootTemp)
			} else {
				_ = os.Remove(tmp)
			}
		}
	}
	if tr.Truncated {
		s.kept.Note = truncationNote(tr, s.kept.Path, false)
	}
	return s.kept
}

func (s *outputCapture) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.limitErr
}

// taggedOutput merges separately piped stdout/stderr in arrival order. Labels
// identify channel switches; stdout-only structured output stays unchanged.
type taggedOutput struct {
	mu      sync.Mutex
	capture *outputCapture
	stderr  bool
}

type outputChannel struct {
	output *taggedOutput
	stderr bool
}

func (w outputChannel) Write(p []byte) (int, error) {
	w.output.mu.Lock()
	defer w.output.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	if w.output.stderr != w.stderr {
		label := "\n[stdout]\n"
		if w.stderr {
			label = "\n[stderr]\n"
		}
		if _, err := w.output.capture.Write([]byte(label)); err != nil {
			return 0, err
		}
		w.output.stderr = w.stderr
	}
	return w.output.capture.Write(p)
}
