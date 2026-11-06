package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func fileAccessFixture(t *testing.T) (account, cwd, sibling, foreign string) {
	t.Helper()
	base := t.TempDir()
	account = filepath.Join(base, "owner-one")
	cwd = filepath.Join(account, "agents", "current")
	sibling = filepath.Join(account, "agents", "other")
	foreign = filepath.Join(base, "owner-two", "agents", "private")
	for _, dir := range []string{cwd, sibling, foreign} {
		mustWrite(t, filepath.Join(dir, "index.html"), "original project")
	}
	return
}

func fileAccessArgs(name, directory string) map[string]any {
	path := filepath.Join(directory, "index.html")
	switch name {
	case "ls":
		return map[string]any{"path": directory}
	case "find":
		return map[string]any{"path": directory, "pattern": "index.html"}
	case "grep":
		return map[string]any{"path": directory, "pattern": "original"}
	case "write":
		return map[string]any{"path": filepath.Join(directory, "new.txt"), "content": "new content"}
	case "edit":
		return map[string]any{"path": path, "edits": []map[string]string{{"oldText": "original", "newText": "updated"}}}
	default:
		return map[string]any{"path": path}
	}
}

func TestFileAccessExistingToolsFollowPolicyWithoutChangingWorkingDirectory(t *testing.T) {
	for _, name := range []string{"read", "ls", "find", "grep", "write", "edit"} {
		t.Run(name, func(t *testing.T) {
			account, cwd, sibling, foreign := fileAccessFixture(t)
			var full atomic.Bool
			registry := New(Options{Cwd: cwd, FileAccess: &FileAccess{Root: account, FullAccess: full.Load}, SpillDir: t.TempDir()})
			tool := find(t, registry, name)
			args := fileAccessArgs(name, sibling)
			if _, err := callTool(t, tool, args); err == nil {
				t.Fatal("ordinary mode accessed a sibling project")
			}
			full.Store(true)
			if _, err := callTool(t, tool, args); err != nil {
				t.Fatal("existing tool did not acquire account-wide access:", err)
			}
			if _, err := callTool(t, tool, fileAccessArgs(name, foreign)); err == nil {
				t.Fatal("full access crossed account boundaries")
			}
			full.Store(false)
			if _, err := callTool(t, tool, args); err == nil || !strings.Contains(err.Error(), "outside the working directory") {
				t.Fatalf("existing tool retained a broader grant: %v", err)
			}
			if got := find(t, registry, name); got != tool {
				t.Fatal("policy change unexpectedly replaced tool objects")
			}
			// Relative paths still refer to the conversation, not the account.
			local := fileAccessArgs(name, cwd)
			if name == "read" || name == "edit" {
				local["path"] = "index.html"
			} else if name == "write" {
				local["path"] = "new.txt"
			} else {
				local["path"] = "."
			}
			if _, err := callTool(t, tool, local); err != nil {
				t.Fatal("relative working-directory behavior changed:", err)
			}
		})
	}
}

func TestFileAccessReadManyTakesOnePolicySnapshot(t *testing.T) {
	account, cwd, sibling, _ := fileAccessFixture(t)
	other := filepath.Join(account, "another", "two.txt")
	mustWrite(t, other, "second original")
	var calls atomic.Int32
	reader := &Read{Cwd: cwd, FileAccess: &FileAccess{Root: account, FullAccess: func() bool { return calls.Add(1) == 1 }}}
	result, err := callTool(t, reader, map[string]any{"paths": []string{filepath.Join(sibling, "index.html"), other}})
	if err != nil || calls.Load() != 1 || !strings.Contains(result.Text, "original project") || !strings.Contains(result.Text, "second original") {
		t.Fatalf("multi-read did not use one dispatch snapshot: calls=%d err=%v text=%q", calls.Load(), err, result.Text)
	}
	if _, err := callTool(t, reader, map[string]any{"path": other}); err == nil {
		t.Fatal("a completed read left its expanded roots on the shared tool")
	}
}

func TestFileAccessSurvivesSkillAndNativeSelectionRefresh(t *testing.T) {
	account, cwd, sibling, _ := fileAccessFixture(t)
	skill := t.TempDir()
	mustWrite(t, filepath.Join(skill, "SKILL.md"), "selected skill")
	var full atomic.Bool
	selection := []string{}
	registry := New(Options{Cwd: cwd, Enabled: &selection, FileAccess: &FileAccess{Root: account, FullAccess: full.Load}, SpillDir: t.TempDir()})
	if _, ok := registry.Get("write"); ok {
		t.Fatal("fixture should withhold write initially")
	}
	refreshed, commit, err := registry.WithSkillContext(SkillContextUpdate{ReadRoots: []string{skill}, Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	commit()
	selection = []string{"write", "edit"}
	refreshed, commit, err = refreshed.WithNativeSelection(&selection, nil)
	if err != nil {
		t.Fatal(err)
	}
	commit()
	full.Store(true)
	for _, name := range []string{"read", "ls", "find", "grep", "write", "edit"} {
		if _, err := callTool(t, find(t, refreshed, name), fileAccessArgs(name, sibling)); err != nil {
			t.Fatalf("%s lost dynamic access through registry refresh: %v", name, err)
		}
	}
	full.Store(false)
	if _, err := callTool(t, find(t, refreshed, "read"), map[string]any{"path": filepath.Join(skill, "SKILL.md")}); err != nil {
		t.Fatal("selected skill root was replaced by the account grant:", err)
	}
	if _, err := callTool(t, find(t, refreshed, "write"), fileAccessArgs("write", sibling)); err == nil {
		t.Fatal("cached native tool kept full access after policy tightened")
	}
}

func TestFileAccessRejectsForeignAliasesAndMisconfiguredRoots(t *testing.T) {
	account, cwd, _, foreign := fileAccessFixture(t)
	alias := filepath.Join(cwd, "outside")
	if err := os.Symlink(foreign, alias); err != nil {
		t.Fatal(err)
	}
	access := &FileAccess{Root: account, FullAccess: func() bool { return true }}
	for _, name := range []string{"read", "write", "edit", "ls", "find", "grep"} {
		registry := New(Options{Cwd: cwd, FileAccess: access, SpillDir: t.TempDir()})
		if _, err := callTool(t, find(t, registry, name), fileAccessArgs(name, alias)); err == nil {
			t.Fatalf("%s followed an alias into another account", name)
		}
	}
	for _, root := range []string{"", "relative", string(filepath.Separator), filepath.Dir(foreign)} {
		_, err := fileAccessRoots(cwd, nil, &FileAccess{Root: root, FullAccess: func() bool { return true }})
		if err == nil {
			t.Fatalf("invalid full-access root accepted: %q", root)
		}
	}
}

func TestFileAccessPreservesManagedDocumentProtection(t *testing.T) {
	account, cwd, sibling, _ := fileAccessFixture(t)
	target := filepath.Join(sibling, "managed.docx")
	mustWrite(t, target, "original managed document")
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	raw, err := json.Marshal(CommandProtectionManifest{Schema: 1, Paths: []string{target}})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, manifest, string(raw))
	registry := New(Options{Cwd: cwd, FileAccess: &FileAccess{Root: account, FullAccess: func() bool { return true }}, CommandProtectionManifest: manifest, SpillDir: t.TempDir()})
	for _, name := range []string{"write", "edit"} {
		args := map[string]any{"path": target, "content": "rewritten", "oldText": "original", "newText": "changed"}
		if _, err := callTool(t, find(t, registry, name), args); err == nil || !strings.Contains(err.Error(), "managed") {
			t.Fatalf("%s bypassed live-document protection: %v", name, err)
		}
	}
	if data, _ := os.ReadFile(target); string(data) != "original managed document" {
		t.Fatal("a rejected mutation changed the managed document")
	}
}

func TestFileAccessDelegationUsesTheRegistryHostGrant(t *testing.T) {
	account, cwd, _, _ := fileAccessFixture(t)
	grant := &FileAccess{Root: account, FullAccess: func() bool { return true }}
	child := &Subagent{Cwd: cwd, FileAccess: &FileAccess{Root: t.TempDir(), FullAccess: func() bool { return true }}}
	registry := New(Options{Cwd: cwd, Subagent: child, FileAccess: grant, SpillDir: t.TempDir()})
	if child.FileAccess != grant {
		t.Fatal("a retained child kept authority from a different host registry")
	}
	refreshed, commit, err := registry.WithSkillContext(SkillContextUpdate{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	commit()
	selection := []string{"write", "edit"}
	_, commit, err = refreshed.WithNativeSelection(&selection, nil)
	if err != nil {
		t.Fatal(err)
	}
	commit()
	if child.FileAccess != grant {
		t.Fatal("a capability refresh lost the child's live parent grant")
	}
	_ = New(Options{Cwd: cwd, Subagent: child, SpillDir: t.TempDir()})
	if child.FileAccess != nil {
		t.Fatal("a registry without an access grant inherited an older host's authority")
	}
}

func TestFileAccessConcurrentPolicyReadsDoNotMutateSharedRoots(t *testing.T) {
	account, cwd, sibling, _ := fileAccessFixture(t)
	var full atomic.Bool
	rootBacking := make([]string, 1, 8)
	rootBacking[0] = t.TempDir()
	reader := &Read{Cwd: cwd, Roots: rootBacking, FileAccess: &FileAccess{Root: account, FullAccess: full.Load}}
	raw, err := json.Marshal(map[string]any{"path": filepath.Join(sibling, "index.html")})
	if err != nil {
		t.Fatal(err)
	}
	var work sync.WaitGroup
	for range 8 {
		work.Add(1)
		go func() {
			defer work.Done()
			for range 30 {
				full.Store(!full.Load())
				_, _ = reader.Execute(context.Background(), raw)
			}
		}()
	}
	work.Wait()
	if len(reader.Roots) != 1 || reader.Roots[0] != rootBacking[0] {
		t.Fatal("concurrent execution changed the tool's configured roots")
	}
}
