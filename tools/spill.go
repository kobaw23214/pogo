package tools

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file answers one question for the whole package: when a tool's output does
// not fit in a result, where does the part the model cannot see go?
//
// Truncation on its own is not a loss — read, ls, find and grep are all truncated
// and lose nothing, because the thing they were reading is still there to be read
// again with a narrower request. The tools that *run* something are different: the
// command has already happened, and output dropped on the floor cannot be asked for
// a second time without paying for the whole run again. bash has spilled to a temp
// file for exactly that reason since the beginning; this generalises that one
// precedent so every tool in the second category gets the same treatment, and so
// that "truncated" and "spilled" are decided together rather than by each caller.
//
// The rule the two halves add up to, and the one worth being able to state in a
// sentence: a tool result is capped at MaxLines/MaxBytes, and when the cap bites,
// either the source is still addressable or the whole output is on disk.

// SpillDirEnv relocates the directory holding full tool output that did not fit
// in a result. Same shape as POGO_SESSION_DIR, POGO_MEMORY_DIR and
// POGO_WORKTREE_DIR, and for the same reason: a deployment decides where state
// lives, and a subagent child inherits the answer through the environment
// rather than through a command line nobody rewrites for it.
const SpillDirEnv = "POGO_SPILL_DIR"

// DefaultSpillDir is where spilled output lives when nothing is configured.
//
// Under the home directory rather than os.TempDir(), and that is the whole point
// of this function. A spill file is worthless unless the read tool can open it,
// read refuses any path outside the working directory and the readable roots, and
// a deployment that sets POGO_READ_ROOTS gets a whitelist that does not include
// /tmp — so the temp directory is the one place a pointer is guaranteed to be
// dead. New adds this directory to the readable roots (see Options.SpillDir), so
// the promise holds wherever it lands; keeping it under ~/.pogo alongside
// sessions and memory also means an operator already knows to look
// there, and a container that mounts the home tree keeps the files across a
// restart.
//
// The temp directory survives only as the last resort for a process with no home
// directory at all, where a readable path is not on offer either way.
func DefaultSpillDir() string {
	if d := strings.TrimSpace(os.Getenv(SpillDirEnv)); d != "" {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".pogo", "spill")
	}
	return filepath.Join(os.TempDir(), "pogo", "spill")
}

// Kept is a truncated view of some output together with the file holding all of
// it. The zero value describes output that fitted: no note, no file.
type Kept struct {
	Truncation
	// Note is the bracketed sentence appended to what the model reads. It is the
	// only thing in the result that says the view was cut short, which makes it
	// the last place allowed to be wrong — see Spill on why a path appears here
	// only when the file behind it is complete.
	Note string
	// Path names the file holding the whole output. Empty when nothing was
	// truncated, or when the write failed. Non-empty is a promise: all of it
	// arrived.
	Path string
}

// Text is what the model reads: the part that fitted, plus the note explaining
// what happened to the rest.
func (k Kept) Text() string { return k.Content + k.Note }

// KeepTail bounds output from the end, for anything that behaves like shell
// output: the exit status and the error are at the bottom, so the bottom is the
// half worth keeping.
func KeepTail(dir, tool, raw string) Kept { return keep(dir, tool, raw, TruncateTail(raw), false) }

// KeepHead bounds output from the start, for anything that behaves like a
// document: a page snapshot, an extracted payload, a rendered report. The first
// screen is the one that orients the model, and a JSON body cut from the front is
// not even recognisable.
func KeepHead(dir, tool, raw string) Kept { return keep(dir, tool, raw, TruncateHead(raw), true) }

func keep(dir, tool, raw string, tr Truncation, head bool) Kept {
	k := Kept{Truncation: tr}
	if !tr.Truncated {
		return k
	}
	// Spilled before the note is written, because the note has to describe what
	// actually happened. Getting this order wrong is how a result ends up
	// promising a complete file that holds a fragment.
	path, ok := Spill(dir, tool, raw)
	if ok {
		k.Path = path
	}
	k.Note = truncationNote(tr, k.Path, head)
	return k
}

func truncationNote(tr Truncation, path string, head bool) string {
	rest := "The rest could not be saved, so only the lines above are available."
	if path != "" {
		// The retrieval instruction is not decoration. A pointer with no
		// instruction is read as infrastructure noise and the content stays
		// invisible; naming the two tools that can open it is what turns
		// "something was cut" into an action. The wording follows the dialect
		// the tools already speak — read ends a truncated file with "Use
		// offset=N to continue" — so this is not a new idea to learn.
		rest = "Full output: " + path + " — read it with offset/limit, or grep it for what you need."
	}
	first, last := 1, tr.OutputLines
	if !head {
		first, last = tr.TotalLines-tr.OutputLines+1, tr.TotalLines
	}
	limit := ""
	if tr.By == "bytes" {
		limit = fmt.Sprintf(" (%s limit)", formatSize(MaxBytes))
	}
	return fmt.Sprintf("\n\n[Showing lines %d-%d of %d%s. %s]",
		first, last, tr.TotalLines, limit, rest)
}

// Spill writes the whole output to a file named after its own content, reporting
// a path only when all of it arrived.
//
// Content addressing rather than a fresh temp name, and it pays for itself three
// times. The note that carries the path is part of the tool result, and the
// result is hashed to detect an agent going in circles (agent.hashToolResults) —
// with a random filename in it, two byte-identical runs hash differently and the
// check can never fire, which is the exact bug that comment warns about for the
// tool_use id. It is also what keeps the prompt prefix stable across turns, the
// same property context editing's placeholder is a pure function for. And it
// deduplicates for free: an agent repeating one large command fills the directory
// with one file rather than one per attempt.
//
// Two writers with the same content race onto the same final name and both win,
// because what they are writing is identical — which is why the write is
// create-temp-then-rename rather than a plain create at the final path, where a
// second writer could be reading a half-written file. The pattern is config.Save's,
// minus its in-place fallback: that exists for single-file bind mounts, and this
// file is one we made ourselves.
//
// Every error is checked and none is reported. Close is checked as carefully as
// the write, because a buffered flush fails there and ignoring it is precisely how
// a partial file passes for a complete one. A file created but not completed is
// removed rather than left behind: nothing points at it, and the next person
// looking through the directory should not find a plausible pogo log quietly
// missing its middle. The caller's fallback is a note that says the rest could not
// be saved — a degraded result, where announcing the truncation and claiming a
// file that is not there would be a wrong one.
func Spill(dir, tool, raw string) (path string, ok bool) {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultSpillDir()
	}
	// Absolute, because the path goes to the model and is resolved against a
	// working directory this function knows nothing about.
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	// 0o700, not the usual 0o755: spilled output is whatever a command printed,
	// which includes whatever secrets it printed.
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", false
	}
	sum := sha256.Sum256([]byte(raw))
	final := filepath.Join(abs, fmt.Sprintf("%s-%x.log", spillPrefix(tool), sum[:6]))

	f, err := os.CreateTemp(abs, ".partial-*")
	if err != nil {
		return "", false
	}
	tmp := f.Name()
	_, werr := f.WriteString(raw)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp)
		return "", false
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", false
	}
	return final, true
}

// spillPrefix makes a tool name safe to put in a filename.
//
// Every name that reaches here is already constrained — the built-ins are
// literals and a manifest tool's name had to match wireNameRE to be registered —
// so this defends against a future name that is not, rather than against any
// caller that exists. A tool name reaching a filesystem path is worth one cheap
// pass either way.
func spillPrefix(tool string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, tool)
	if safe = strings.Trim(safe, "-"); safe == "" {
		return "tool"
	}
	if len(safe) > 32 {
		safe = safe[:32]
	}
	return safe
}
