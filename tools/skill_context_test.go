package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yosukeno/pogo/skillruntime"
)

func TestSkillContextPreservesStatefulToolsAndReplacesReadAndChildGrants(t *testing.T) {
	cwd, oldRoot, newRoot := t.TempDir(), t.TempDir(), t.TempDir()
	for _, root := range []string{oldRoot, newRoot} {
		if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("note"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	child := &Subagent{Cwd: cwd, SkillPaths: []string{oldRoot}, SkillReadRoots: []string{oldRoot}, ExplicitSkills: true}
	office := &Office{}
	r := New(Options{Cwd: cwd, ReadRoots: []string{oldRoot}, Todo: true, Subagent: child, Synth: []Tool{office}})
	todo, _ := r.Get("todo")
	next, apply, err := r.WithSkillContext(SkillContextUpdate{Previous: []Tool{office}, Tools: []Tool{office}, ReadRoots: []string{newRoot}, ChildReadRoots: []string{newRoot}, SkillPaths: []string{newRoot}, ExplicitSkills: true, Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(child.SkillPaths, []string{oldRoot}) {
		t.Fatal("prepared update changed child before durable commit")
	}
	apply()
	if now, _ := next.Get("todo"); now != todo {
		t.Fatal("todo state recreated")
	}
	if now, _ := next.Get(OfficeToolName); now != office {
		t.Fatal("Office bridge recreated")
	}
	if now, _ := next.Get("subagent"); now != child {
		t.Fatal("retained child context recreated")
	}
	if !slices.Equal(child.SkillPaths, []string{newRoot}) || !slices.Equal(child.SkillReadRoots, []string{newRoot}) {
		t.Fatal("child retains old read grant")
	}
	read, _ := next.Get("read")
	args, _ := json.Marshal(map[string]string{"path": filepath.Join(newRoot, "note.txt")})
	if _, err := read.Execute(context.Background(), args); err != nil {
		t.Fatal("new root not readable", err)
	}
	args, _ = json.Marshal(map[string]string{"path": filepath.Join(oldRoot, "note.txt")})
	if _, err := read.Execute(context.Background(), args); err == nil {
		t.Fatal("old root stayed readable")
	}
	original, _ := r.Get("read")
	if _, err := original.Execute(context.Background(), args); err != nil {
		t.Fatal("old registry mutated", err)
	}
}
func TestSkillContextRejectsNativeNameCollisionBeforeApply(t *testing.T) {
	r := New(Options{Cwd: t.TempDir(), Todo: true})
	if _, _, err := r.WithSkillContext(SkillContextUpdate{Tools: []Tool{&Todo{}}}); err == nil {
		t.Fatal("native tool collision accepted")
	}
}

func TestSkillContextKeepsHostReaderAcrossRuntimeProjectionChanges(t *testing.T) {
	oldRuntime := &RuntimeSkillRead{Session: &skillruntime.BoundSession{}}
	newRuntime := &RuntimeSkillRead{Session: &skillruntime.BoundSession{}}
	r := New(Options{Cwd: t.TempDir(), SkillAuthor: &skillAuthorFixture{}, Synth: []Tool{oldRuntime}})
	hostReader, ok := r.Get(SkillReadName)
	if !ok || !skillAuthorBackendAvailable(hostReader.(*RuntimeSkillRead).Backend) {
		t.Fatal("missing host reader")
	}
	for _, update := range []SkillContextUpdate{
		{Previous: []Tool{oldRuntime}, Tools: []Tool{newRuntime}},
		{Previous: []Tool{newRuntime}},
		{Tools: []Tool{oldRuntime}},
	} {
		next, apply, err := r.WithSkillContext(update)
		if err != nil {
			t.Fatal(err)
		}
		apply()
		if got, ok := next.Get(SkillReadName); !ok || got != hostReader {
			t.Fatal("runtime projection removed or replaced the host reader")
		}
		count := 0
		for _, tool := range next.All() {
			if tool.Name() == SkillReadName {
				count++
			}
		}
		if count != 1 {
			t.Fatal("runtime projection duplicated the host reader", count)
		}
		r = next
	}
}

func TestSkillContextRebindsRuntimeReaderWithoutAuthoringHost(t *testing.T) {
	oldSession, newSession := &skillruntime.BoundSession{}, &skillruntime.BoundSession{}
	oldReader, newReader := &RuntimeSkillRead{Session: oldSession}, &RuntimeSkillRead{Session: newSession}
	r := New(Options{Cwd: t.TempDir(), Synth: []Tool{oldReader}})
	next, apply, err := r.WithSkillContext(SkillContextUpdate{Previous: []Tool{oldReader}, Tools: []Tool{newReader}})
	if err != nil {
		t.Fatal(err)
	}
	apply()
	if got, ok := next.Get(SkillReadName); !ok || got != newReader || got.(*RuntimeSkillRead).Session != newSession {
		t.Fatal("new runtime reader retained the old bound session")
	}
	if got, _ := r.Get(SkillReadName); got != oldReader || got.(*RuntimeSkillRead).Session != oldSession {
		t.Fatal("old run snapshot mutated")
	}
	count := 0
	for _, tool := range next.All() {
		if tool.Name() == SkillReadName {
			count++
		}
	}
	if count != 1 {
		t.Fatal("runtime rebind duplicated reader", count)
	}
}
