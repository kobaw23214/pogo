package tools

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOutputCaptureMatchesTailAndSpillsOriginal(t *testing.T) {
	for _, raw := range []string{
		"", "small\n", strings.Repeat("line\n", MaxLines+10),
		strings.Repeat("x", MaxBytes+100), strings.Repeat("x", MaxBytes+100) + "\n",
		strings.Repeat("very long first line", 6000) + "\nlast\n",
		strings.Repeat("中文一行\n", 9000), strings.Repeat("中", MaxBytes) + "\n",
		strings.Repeat("a\n", MaxBytes) + "last", strings.Repeat("abcd\n", 12000),
	} {
		for _, chunk := range []int{1, 4096, MaxBytes + 1, 100000} {
			capture := newOutputCapture(t.TempDir(), "probe", nil)
			for at := 0; at < len(raw); at += chunk {
				if _, err := capture.Write([]byte(raw[at:min(at+chunk, len(raw))])); err != nil {
					t.Fatal(err)
				}
				if len(capture.tail) > MaxBytes+1 || cap(capture.tail) > MaxBytes+1 {
					t.Fatal("tail grew beyond its memory limit")
				}
			}
			k := capture.finish()
			want := TruncateTail(raw)
			if k.Content != want.Content || k.TotalLines != want.TotalLines || k.OutputLines != want.OutputLines || k.Truncated != want.Truncated {
				t.Fatalf("len=%d chunk=%d: got content bytes=%d lines=%d/%d truncated=%v, want bytes=%d lines=%d/%d truncated=%v", len(raw), chunk, len(k.Content), k.OutputLines, k.TotalLines, k.Truncated, len(want.Content), want.OutputLines, want.TotalLines, want.Truncated)
			}
			if !utf8.ValidString(k.Content) {
				t.Fatal("invalid UTF8 in tail")
			}
			if want.Truncated {
				full, err := os.ReadFile(k.Path)
				if err != nil || string(full) != raw {
					t.Fatalf("full output differs: %v", err)
				}
			} else if files, _ := filepath.Glob(filepath.Join(capture.dir, "*")); len(files) != 0 {
				t.Fatal("small output left a spill file")
			}
		}
	}
}

func TestCommandOutputQuotaCancelsAndNeverPublishesPartialFile(t *testing.T) {
	dir := t.TempDir()
	cancelled := false
	capture := newOutputCapture(dir, "test", func() { cancelled = true })
	capture.limit = 2 * MaxBytes
	written, err := capture.Write(bytes.Repeat([]byte{'x'}, int(capture.limit)+1))
	if !errors.Is(err, ErrCommandOutputLimit) || int64(written) != capture.limit || !cancelled {
		t.Fatalf("n=%d err=%v cancelled=%v", written, err, cancelled)
	}
	k := capture.finish()
	if k.Path != "" || !k.Truncated || !strings.Contains(k.Note, "could not be saved") {
		t.Fatalf("incomplete capture misrepresented: %+v", k)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("partial file leaked")
	}
	if _, err := capture.Write([]byte("late")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
}

func TestOutputCaptureDiskFailureStaysBounded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	capture := newOutputCapture(dir, "test", nil)
	raw := strings.Repeat("line\n", 30000)
	if _, err := capture.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	k := capture.finish()
	if k.Path != "" || !strings.Contains(k.Note, "could not be saved") || k.Content != TruncateTail(raw).Content || len(capture.tail) > MaxBytes+1 {
		t.Fatal("disk failure lost bounded, honest tail")
	}
}

func TestOutputCaptureReusesCompleteContentAddressedSpill(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(strings.Repeat("same line\n", 3000))
	var first string
	for i := 0; i < 2; i++ {
		capture := newOutputCapture(dir, "repeat", nil)
		if _, err := capture.Write(raw); err != nil {
			t.Fatal(err)
		}
		k := capture.finish()
		if k.Path == "" {
			t.Fatal("repeat output lost its full-log link")
		}
		if i == 0 {
			first = k.Path
		} else if k.Path != first {
			t.Fatal("same output acquired a different identity")
		}
		full, err := os.ReadFile(k.Path)
		if err != nil || !bytes.Equal(full, raw) {
			t.Fatalf("repeat spill incomplete: %v", err)
		}
	}
}

func TestStreamingBudgetStopsPendingBufferGrowth(t *testing.T) {
	s := testPartialSink(t, func(Partial) {})
	chunk := bytes.Repeat([]byte{'x'}, 100000)
	for i := 0; i < 25; i++ {
		if _, err := s.Write(chunk); err != nil {
			t.Fatal(err)
		}
		if s.pending.Len() > partialChunk || s.pending.Cap() > 2*partialChunk || len(s.output.tail) > MaxBytes+1 {
			t.Fatal("stream buffer grew beyond budget")
		}
	}
	if !s.capped || s.pending.Len() != 0 {
		t.Fatal("capped stream retained pending output")
	}
	k := s.close()
	info, err := os.Stat(k.Path)
	if err != nil || info.Size() != int64(len(chunk)*25) {
		t.Fatalf("full output incomplete: %v", err)
	}
}

func TestTaggedOutputKeepsStructuredStdoutAndLabelsStderr(t *testing.T) {
	capture := newOutputCapture(t.TempDir(), "test", nil)
	out := &taggedOutput{capture: capture}
	stdout := outputChannel{output: out}
	stderr := outputChannel{output: out, stderr: true}
	_, _ = stdout.Write([]byte(`{"ok":true}`))
	_, _ = stderr.Write([]byte("warning"))
	_, _ = stdout.Write([]byte("next"))
	if got := capture.finish().Content; got != "{\"ok\":true}\n[stderr]\nwarning\n[stdout]\nnext" {
		t.Fatalf("merged=%q", got)
	}
}
