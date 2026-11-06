package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Guard catches common mistakes in delegated shell commands. It is not an OS
// sandbox: scripts and external programs can evade command-text checks.
type Guard struct {
	Workspace  string
	FileAccess *FileAccess
}

// deniedEnv are assignments that redirect git regardless of any path check.
var deniedEnv = []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_COMMON_DIR=", "GIT_CONFIG"}

// Check reports why a command may not run, or nil.
//
// The messages say what to do instead. A refusal the model cannot act on turns
// into three more attempts at the same thing.
func (g *Guard) Check(command string) error {
	if g == nil {
		return nil
	}
	for _, word := range shellWords(command) {
		if isGit(word) {
			return fmt.Errorf("git is not available to a subagent. Your changes are already in place; leave the files in the workspace and report their paths. (refused: %s)", firstLine(command))
		}
	}
	for _, env := range deniedEnv {
		if strings.Contains(command, env) {
			return fmt.Errorf("setting %s would point git at another repository, which a "+
				"subagent may not do. (refused: %s)", strings.TrimSuffix(env, "="), firstLine(command))
		}
	}
	if dir := absoluteCD(command); dir != "" && !g.inside(dir) {
		return fmt.Errorf("changing directory to %s would leave your workspace. Everything "+
			"you need is under %s. (refused: %s)", dir, g.Workspace, firstLine(command))
	}
	return nil
}

// CheckDir reports why a command may not run in dir, or nil.
//
// It exists because bash's workdir parameter reaches the same place a `cd` does
// without going through the command text, and Check finds a `cd` by parsing that
// text. Without this, adding the parameter would have handed a subagent a way out of
// its workspace that reads as an ordinary argument.
//
// resolved is what will be passed to the process and is what the comparison uses;
// requested is what the model wrote, and appears in the message because that is the
// string it has to change.
func (g *Guard) CheckDir(resolved, requested string) error {
	if g == nil || requested == "" {
		return nil
	}
	if !g.inside(resolved) {
		return fmt.Errorf("workdir %q resolves to %s, which is outside your workspace. "+
			"Everything you need is under %s", requested, resolved, g.Workspace)
	}
	return nil
}

// inside reports whether a `cd` target stays in the workspace.
//
// Shares within with the path guard rather than repeating the comparison, because
// it had the same case-sensitivity defect for the same reason: a `cd` typed with
// different case than the workspace's real spelling was refused on a
// case-insensitive volume, and the refusal named two paths a reader would say were
// identical. One predicate means the next repair lands in both places.
//
// Not canonicalised first, deliberately. The argument comes out of the command
// text, and resolving it would answer a question about a different string than the
// one bash will run.
func (g *Guard) inside(dir string) bool {
	if within(filepath.Clean(dir), filepath.Clean(g.Workspace)) {
		return true
	}
	roots, err := fileAccessRoots(g.Workspace, nil, g.FileAccess)
	if err != nil {
		return false
	}
	for _, root := range roots {
		if within(canonical(dir), canonical(root)) {
			return true
		}
	}
	return false
}

// isGit matches the command name whether it was called plainly or by path.
func isGit(word string) bool {
	word = strings.Trim(word, `"'`)
	if word == "git" {
		return true
	}
	return filepath.Base(word) == "git" && strings.ContainsRune(word, filepath.Separator)
}

// shellWords splits on whitespace and the operators that start a new command, so
// that `ls && git log` is seen as two commands rather than one string that merely
// contains the word git.
//
// Not a shell parser, and not trying to be: see the type comment. It exists to
// catch the command the model actually wrote, not to survive deliberate quoting.
func shellWords(command string) []string {
	fields := strings.FieldsFunc(command, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', ';', '|', '&', '(', ')', '`':
			return true
		}
		return false
	})
	return fields
}

// absoluteCD returns the target of a `cd /abs/path`, or "".
//
// Only absolute targets are checked. A relative `cd ..` can also climb out, but
// the path guard already refuses to read or write outside the workspace, so the
// case worth catching is the one that jumps somewhere specific.
func absoluteCD(command string) string {
	words := shellWords(command)
	for i, w := range words {
		if w != "cd" || i+1 >= len(words) {
			continue
		}
		target := strings.Trim(words[i+1], `"'`)
		if filepath.IsAbs(target) {
			return target
		}
	}
	return ""
}
