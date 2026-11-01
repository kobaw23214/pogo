package tools

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// The scope's path normalization is the boundary itself. These tests deliberately
// use real temporary directories: lexical checks alone accept a symlink whose
// destination is outside the advertised Cwd.
func TestCommandScopeNormalizesWorkdirWithinCwd(t *testing.T) {
	base := commandScopeTempDir(t)
	cwd := commandScopeMkdir(t, filepath.Join(base, "workspace"))
	work := commandScopeMkdir(t, filepath.Join(cwd, "project", "work"))
	root := commandScopeSkillRoot(t, filepath.Join(base, "skills", "one"))

	scope := &CommandScope{
		Cwd:         cwd,
		ReadRoots:   []string{root},
		Network:     true,
		Interactive: true,
	}
	spec, err := normalizedCommandScope(scope, work)
	if err != nil {
		t.Fatalf("workdir inside Cwd was rejected: %v", err)
	}
	if got := filepath.Clean(spec.cwd); got != commandScopeCanonical(t, cwd) {
		t.Errorf("spec.Cwd = %q, want %q", got, cwd)
	}
	if got := filepath.Clean(spec.workdir); got != commandScopeCanonical(t, work) {
		t.Errorf("spec.Workdir = %q, want %q", got, work)
	}
	if len(spec.readRoots) != 1 {
		t.Fatalf("read roots = %v, want one concrete skill directory", spec.readRoots)
	}
	if got := filepath.Clean(spec.readRoots[0]); got != commandScopeCanonical(t, root) {
		t.Errorf("spec.readRoots[0] = %q, want %q", got, root)
	}
	if !spec.network || !spec.interactive {
		t.Errorf("network/interactive flags changed during normalization: %+v", spec)
	}

	// Cwd is also a valid place to start; containment means “inside or equal”.
	spec, err = normalizedCommandScope(scope, cwd)
	if err != nil {
		t.Fatalf("workdir equal to Cwd was rejected: %v", err)
	}
	if got, want := filepath.Clean(spec.workdir), commandScopeCanonical(t, cwd); got != want {
		t.Errorf("workdir equal to Cwd became %q, want %q", got, want)
	}
	for _, requested := range []string{"", ".", filepath.Join("project", "work")} {
		got, err := normalizedCommandScope(scope, requested)
		want := cwd
		if requested != "" && requested != "." {
			want = work
		}
		if err != nil || got.workdir != want {
			t.Errorf("workdir %q = %q, %v; want %q", requested, got.workdir, err, want)
		}
	}

	outside := commandScopeTempDir(t)
	for _, tc := range []struct {
		name    string
		workdir string
	}{
		{"parent of Cwd", filepath.Dir(cwd)},
		{"relative parent escape", ".."},
		{"sibling outside Cwd", outside},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := normalizedCommandScope(scope, tc.workdir); err == nil {
				t.Errorf("workdir %q was accepted outside Cwd %q", tc.workdir, cwd)
			}
		})
	}

	t.Run("symlink escape", func(t *testing.T) {
		escape := filepath.Join(cwd, "escape")
		commandScopeSymlink(t, escape, outside)
		if _, err := normalizedCommandScope(scope, escape); err == nil {
			t.Errorf("symlink %q escaped Cwd %q but was accepted", escape, cwd)
		}
	})
}

func TestCommandScopeRejectsInvalidWorkspace(t *testing.T) {
	base := commandScopeTempDir(t)
	notDirectory := filepath.Join(base, "file")
	commandScopeWriteFile(t, notDirectory, "fixture")
	for _, tc := range []struct {
		name  string
		scope *CommandScope
	}{
		{"nil", nil},
		{"empty", &CommandScope{}},
		{"relative", &CommandScope{Cwd: "relative-workspace"}},
		{"file", &CommandScope{Cwd: notDirectory}},
		{"missing", &CommandScope{Cwd: filepath.Join(base, "missing")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := normalizedCommandScope(tc.scope, ""); err == nil {
				t.Fatal("invalid workspace accepted through the default workdir path")
			}
		})
	}
}

// ReadRoots grant access to skill instructions, not to the workspace or to a
// broad ancestor that happens to contain one SKILL.md.
func TestCommandScopeRejectsInvalidReadRoots(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(t *testing.T) (*CommandScope, string)
	}{
		{"missing root", func(t *testing.T) (*CommandScope, string) {
			base := commandScopeTempDir(t)
			return commandScopeScopeWithWorkspace(t, base, filepath.Join(base, "missing")), filepath.Join(base, "work")
		}},
		{"root is a regular file", func(t *testing.T) (*CommandScope, string) {
			base := commandScopeTempDir(t)
			notDir := filepath.Join(base, "not-a-directory")
			commandScopeWriteFile(t, notDir, "not a root")
			return commandScopeScopeWithWorkspace(t, base, notDir), filepath.Join(base, "work")
		}},
		{"root has no SKILL.md", func(t *testing.T) (*CommandScope, string) {
			base := commandScopeTempDir(t)
			root := commandScopeMkdir(t, filepath.Join(base, "skills", "empty"))
			return commandScopeScopeWithWorkspace(t, base, root), filepath.Join(base, "work")
		}},
		{"SKILL.md is a directory", func(t *testing.T) (*CommandScope, string) {
			base := commandScopeTempDir(t)
			root := commandScopeMkdir(t, filepath.Join(base, "skills", "directory-skill"))
			commandScopeMkdir(t, filepath.Join(root, "SKILL.md"))
			return commandScopeScopeWithWorkspace(t, base, root), filepath.Join(base, "work")
		}},
		{"root is inside the workspace", func(t *testing.T) (*CommandScope, string) {
			base := commandScopeTempDir(t)
			work := commandScopeMkdir(t, filepath.Join(base, "work"))
			root := commandScopeSkillRoot(t, filepath.Join(work, "skill"))
			scope := &CommandScope{Cwd: work, ReadRoots: []string{root}}
			return scope, work
		}},
		{"root is the workspace", func(t *testing.T) (*CommandScope, string) {
			base := commandScopeTempDir(t)
			work := commandScopeMkdir(t, filepath.Join(base, "work"))
			commandScopeWriteFile(t, filepath.Join(work, "SKILL.md"), "workspace is not a skill bundle")
			scope := &CommandScope{Cwd: work, ReadRoots: []string{work}}
			return scope, work
		}},
		{"root is an ancestor of the workspace", func(t *testing.T) (*CommandScope, string) {
			base := commandScopeTempDir(t)
			work := commandScopeMkdir(t, filepath.Join(base, "work"))
			commandScopeWriteFile(t, filepath.Join(base, "SKILL.md"), "too broad")
			scope := &CommandScope{Cwd: work, ReadRoots: []string{base}}
			return scope, work
		}},
		{"roots overlap", func(t *testing.T) (*CommandScope, string) {
			base := commandScopeTempDir(t)
			parent := commandScopeSkillRoot(t, filepath.Join(base, "skills", "parent"))
			child := commandScopeSkillRoot(t, filepath.Join(parent, "child"))
			scope := commandScopeScopeWithWorkspace(t, base, parent)
			scope.ReadRoots = []string{parent, child}
			return scope, filepath.Join(base, "work")
		}},
		{"roots are duplicates", func(t *testing.T) (*CommandScope, string) {
			base := commandScopeTempDir(t)
			root := commandScopeSkillRoot(t, filepath.Join(base, "skills", "one"))
			scope := commandScopeScopeWithWorkspace(t, base, root)
			scope.ReadRoots = []string{root, root}
			return scope, filepath.Join(base, "work")
		}},
		{"private agent state", func(t *testing.T) (*CommandScope, string) {
			base := commandScopeTempDir(t)
			root := commandScopeSkillRoot(t, filepath.Join(base, ".pogo", "skill"))
			return commandScopeScopeWithWorkspace(t, base, root), filepath.Join(base, "work")
		}},
		{"root is a symlink alias", func(t *testing.T) (*CommandScope, string) {
			base := commandScopeTempDir(t)
			root := commandScopeSkillRoot(t, filepath.Join(base, "skills", "canonical"))
			alias := filepath.Join(base, "skill-alias")
			commandScopeSymlink(t, alias, root)
			return commandScopeScopeWithWorkspace(t, base, alias), filepath.Join(base, "work")
		}},
		{"SKILL.md is a symlink", func(t *testing.T) (*CommandScope, string) {
			base := commandScopeTempDir(t)
			root := commandScopeMkdir(t, filepath.Join(base, "skills", "one"))
			marker := filepath.Join(base, "outside-instructions.md")
			commandScopeWriteFile(t, marker, "fixture")
			commandScopeSymlink(t, filepath.Join(root, "SKILL.md"), marker)
			return commandScopeScopeWithWorkspace(t, base, root), filepath.Join(base, "work")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope, work := tc.make(t)
			if _, err := normalizedCommandScope(scope, work); err == nil {
				t.Fatalf("invalid ReadRoots %q were accepted", scope.ReadRoots)
			}
		})
	}
}

func TestCommandScopeScopedEnvironmentIsPrivate(t *testing.T) {
	cwd := commandScopeTempDir(t)
	supplied := []string{
		"PATH=/command-scope/supplied-path",
		"HOME=/host/home",
		"TMPDIR=/host/tmp",
		"XDG_CONFIG_HOME=/host/config",
		"COMMAND_SCOPE_SECRET=must-not-cross",
		"OPENAI_API_KEY=sk-command-scope-not-real",
		"ANTHROPIC_API_KEY=sk-ant-command-scope-not-real",
		"BASH_ENV=/host/hooks/bash",
		"ENV=/host/hooks/env",
		"LD_PRELOAD=/host/lib/libhook.so",
		"LD_LIBRARY_PATH=/host/lib",
		"POGO_SESSION_TOKEN=not-real",
		"POGO_ANY_VALUE=not-real",
	}

	env := scopedEnvironment(supplied, cwd)
	values := commandScopeEnvValues(t, env)
	if paths := values["PATH"]; len(paths) != 1 || paths[0] == "" || paths[0] == "/command-scope/supplied-path" {
		t.Fatalf("PATH values = %q, want exactly one nonempty fixed value rather than the supplied value", paths)
	}
	fixedPath := values["PATH"][0]
	other := scopedEnvironment([]string{"PATH=/another/supplied/path"}, t.TempDir())
	if got := commandScopeEnvValues(t, other)["PATH"]; len(got) != 1 || got[0] != fixedPath {
		t.Fatalf("fixed PATH changed to %q, first call returned %q", got, fixedPath)
	}

	for _, name := range []string{
		"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME",
	} {
		got, ok := values[name]
		if !ok || len(got) != 1 {
			t.Fatalf("%s must be set exactly once; got %q", name, got)
		}
		if !commandScopeUnder(cwd, got[0]) {
			t.Errorf("%s = %q, want a private path under %q", name, got[0], cwd)
		}
	}
	// These paths are private tmpfs mounts in scopedBwrapArgs, not host paths.
	for name, want := range map[string]string{"TMPDIR": "/tmp", "TMP": "/tmp", "TEMP": "/tmp", "XDG_RUNTIME_DIR": "/run/user"} {
		if got := values[name]; len(got) != 1 || got[0] != want {
			t.Errorf("%s = %q, want private namespace path %q", name, got, want)
		}
	}

	for _, name := range []string{
		"COMMAND_SCOPE_SECRET", "OPENAI_API_KEY", "ANTHROPIC_API_KEY",
		"BASH_ENV", "ENV", "LD_PRELOAD", "LD_LIBRARY_PATH",
	} {
		if got, ok := values[name]; ok {
			t.Errorf("%s crossed the command environment as %q", name, got)
		}
	}
	for name := range values {
		if strings.HasPrefix(name, "POGO_") {
			t.Errorf("POGO_ variable %s crossed the command environment", name)
		}
	}
}

func TestCommandScopeBwrapArgs(t *testing.T) {
	spec := commandScopeSpec{
		cwd:       "/workspace/project",
		workdir:   "/workspace/project/subdir",
		readRoots: []string{"/skills/one", "/skills/two"},
	}
	binds := []scopeBind{
		{source: "/runtime/usr", target: "/usr"},
		{source: "/runtime/libs", target: "/lib"},
	}
	argv := []string{"/bin/bash", "-c", "printf '%s' 'one two; $HOME'"}
	args := scopedBwrapArgs(spec, binds, argv)

	for _, want := range [][]string{
		{"--unshare-user"},
		{"--unshare-pid"},
		{"--die-with-parent"},
		{"--cap-drop", "ALL"},
		{"--clearenv"},
		{"--proc", "/proc"},
		{"--dev", "/dev"},
		{"--tmpfs", "/tmp"},
		{"--tmpfs", "/run"},
		{"--dir", "/run/user"},
		{"--ro-bind", "/runtime/usr", "/usr"},
		{"--ro-bind", "/runtime/libs", "/lib"},
		{"--ro-bind", "/skills/one", "/skills/one"},
		{"--ro-bind", "/skills/two", "/skills/two"},
		{"--bind", spec.cwd, spec.cwd},
		{"--remount-ro", "/"},
		{"--unshare-net"},
		{"--new-session"},
	} {
		if commandScopeArgvIndex(args, want...) < 0 {
			t.Errorf("bwrap args are missing %v\ngot: %q", want, args)
		}
	}
	if commandScopeArgvIndex(args, "--chdir", "/workspace/project/subdir") < 0 {
		t.Errorf("bwrap args do not chdir to the scoped workdir\ngot: %q", args)
	}
	for _, source := range []string{"/", "/proc", "/dev", "/tmp", "/run", "/home", "/workspace"} {
		if commandScopeHasBind(args, source, source) {
			t.Errorf("host path %q is bound onto itself instead of remaining private: %q", source, args)
		}
	}
	if sep := commandScopeArgvIndex(args, "--"); sep < 0 || !slices.Equal(args[sep+1:], argv) {
		t.Errorf("command argv was not preserved after --\ngot: %q\nwant: %q", args, argv)
	}

	networked := spec
	networked.network = true
	networkedArgs := scopedBwrapArgs(networked, binds, argv)
	if commandScopeArgvIndex(networkedArgs, "--unshare-net") >= 0 {
		t.Errorf("Network=true still includes --unshare-net: %q", networkedArgs)
	}
	if sep := commandScopeArgvIndex(networkedArgs, "--"); sep < 0 || !slices.Equal(networkedArgs[sep+1:], argv) {
		t.Errorf("Network=true changed the command after --\ngot: %q", networkedArgs)
	}

	interactive := spec
	interactive.interactive = true
	interactiveArgs := scopedBwrapArgs(interactive, binds, argv)
	if commandScopeArgvIndex(interactiveArgs, "--new-session") >= 0 {
		t.Errorf("Interactive=true still includes --new-session: %q", interactiveArgs)
	}
	if sep := commandScopeArgvIndex(interactiveArgs, "--"); sep < 0 || !slices.Equal(interactiveArgs[sep+1:], argv) {
		t.Errorf("Interactive=true changed the command after --\ngot: %q", interactiveArgs)
	}
}

// WrapScopedCommand prepares a command; it must never turn an unsupported
// platform into an unconfined launch. Process is nil until Start or Run, so it
// is a portable witness that no launch was attempted.
func TestCommandScopeWrapScopedCommandRefusesOutsideLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("command wrapping is supported on Linux")
	}
	base := commandScopeTempDir(t)
	cwd := commandScopeMkdir(t, filepath.Join(base, "workspace"))
	work := commandScopeMkdir(t, filepath.Join(cwd, "work"))
	root := commandScopeSkillRoot(t, filepath.Join(base, "skills", "one"))
	if _, err := normalizedCommandScope(&CommandScope{Cwd: cwd, ReadRoots: []string{root}}, work); err != nil {
		t.Fatalf("fixture scope is invalid: %v", err)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^$")
	cmd.Dir = work
	if err := WrapScopedCommand(cmd, &CommandScope{Cwd: cwd, ReadRoots: []string{root}}); err == nil {
		t.Fatal("WrapScopedCommand succeeded on a non-Linux platform")
	}
	if cmd.Process != nil {
		t.Fatalf("WrapScopedCommand launched the command before returning its platform error (pid=%d)", cmd.Process.Pid)
	}
}

func commandScopeMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func commandScopeTempDir(t *testing.T) string {
	t.Helper()
	return commandScopeCanonical(t, t.TempDir())
}

func commandScopeWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commandScopeSkillRoot(t *testing.T, path string) string {
	t.Helper()
	commandScopeMkdir(t, path)
	commandScopeWriteFile(t, filepath.Join(path, "SKILL.md"), "# command-scope test skill\n")
	return path
}

func commandScopeScopeWithWorkspace(t *testing.T, base, root string) *CommandScope {
	t.Helper()
	cwd := commandScopeMkdir(t, filepath.Join(base, "work"))
	return &CommandScope{Cwd: cwd, ReadRoots: []string{root}}
}

func commandScopeCanonical(t *testing.T, path string) string {
	t.Helper()
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(absolute)
}

func commandScopeSymlink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, fs.ErrPermission) || (runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314))) {
			t.Skipf("host disallows symlink creation: %v", err)
		}
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink", link)
	}
}

func commandScopeEnvValues(t *testing.T, env []string) map[string][]string {
	t.Helper()
	values := make(map[string][]string)
	for _, entry := range env {
		name, value, found := strings.Cut(entry, "=")
		if !found || name == "" {
			t.Fatalf("malformed environment entry %q", entry)
		}
		values[name] = append(values[name], value)
	}
	return values
}

func commandScopeUnder(parent, child string) bool {
	relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	if err != nil {
		return false
	}
	return relative != ".." && !filepath.IsAbs(relative) &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func commandScopeArgvIndex(argv []string, want ...string) int {
	for i := 0; i+len(want) <= len(argv); i++ {
		if slices.Equal(argv[i:i+len(want)], want) {
			return i
		}
	}
	return -1
}

func commandScopeHasBind(argv []string, source, target string) bool {
	for _, flag := range []string{
		"--ro-bind", "--ro-bind-try", "--bind", "--bind-try", "--dev-bind", "--dev-bind-try",
	} {
		if commandScopeArgvIndex(argv, flag, source, target) >= 0 {
			return true
		}
	}
	return false
}
