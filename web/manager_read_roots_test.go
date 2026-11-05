package web

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

// Managers and hot reloads live in one server process. A skill root belongs to
// its Manager's snapshot, not the environment inherited by every future exec.
func TestManagerSkillViewsDoNotAccumulateProcessReadRoots(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		name := "unset"
		if inherited {
			name = "operator-roots"
		}
		t.Run(name, func(t *testing.T) {
			operator := t.TempDir()
			t.Setenv(tools.EnvReadRoots, operator)
			if !inherited {
				if err := os.Unsetenv(tools.EnvReadRoots); err != nil {
					t.Fatal(err)
				}
			}
			before, present := os.LookupEnv(tools.EnvReadRoots)
			var operatorRoots []string
			if inherited {
				operatorRoots = []string{operator}
			}
			checkEnvironment := func(stage string) {
				t.Helper()
				if got, exists := os.LookupEnv(tools.EnvReadRoots); got != before || exists != present {
					t.Errorf("%s changed process read roots: bytes=%d, present=%v; want bytes=%d, present=%v",
						stage, len(got), exists, len(before), present)
				}
			}
			first := writeTestSkill(t, t.TempDir(), "first", "First skill", "# First")
			second := writeTestSkill(t, t.TempDir(), "second", "Second skill", "# Second")
			reloaded := writeTestSkill(t, t.TempDir(), "reloaded", "Reloaded skill", "# Reloaded")
			one := &Manager{cfg: Config{ReadRoots: operatorRoots}}
			two := &Manager{cfg: Config{ReadRoots: operatorRoots}}
			one.SetSkills([]skills.Skill{first})
			checkEnvironment("first Manager")
			two.SetSkills([]skills.Skill{second})
			checkEnvironment("second Manager")
			if got, want := two.skillViews().readRoots, append([]string{second.Dir}, operatorRoots...); !reflect.DeepEqual(got, want) {
				t.Fatalf("second Manager roots=%v, want only its skill and operator roots %v", got, want)
			}
			one.SetSkills([]skills.Skill{reloaded})
			checkEnvironment("skill reload")
			if got, want := one.skillViews().readRoots, append([]string{reloaded.Dir}, operatorRoots...); !reflect.DeepEqual(got, want) {
				t.Fatalf("reloaded roots=%v, want %v (old skill must be removed)", got, want)
			}
			if got, want := two.skillViews().readRoots, append([]string{second.Dir}, operatorRoots...); !reflect.DeepEqual(got, want) {
				t.Fatalf("reload changed another Manager's roots: got %v, want %v", got, want)
			}
			one.SetSkills(nil)
			checkEnvironment("all skills removed")
			if got := one.skillViews().readRoots; len(got) != len(operatorRoots) || inherited && got[0] != operator {
				t.Fatalf("removing skills lost operator roots or retained a skill: %v", got)
			}
		})
	}
}

// The subprocess boundary must keep working without a process-global union:
// ordinary children reload -skill paths and inherit operator roots; an explicit
// selection receives its complete roots in cmd.Env and disables auto discovery.
func TestManagerSkillViewsTravelToChildrenWithoutChangingProcessReadRoots(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("subagent fixture uses a Unix shell script")
	}
	operator := t.TempDir()
	t.Setenv(tools.EnvReadRoots, operator)
	sk := writeTestSkill(t, t.TempDir(), "child-skill", "Child skill", "# Child")
	m := &Manager{cfg: Config{ReadRoots: []string{operator}}}
	m.SetSkills([]skills.Skill{sk})
	view := m.skillViews()
	program := filepath.Join(t.TempDir(), "child")
	script := `#!/bin/sh
printf '%s\n' '{"type":"turn_start","turn":1}'
printf '{"type":"token","text":"roots=%s\\n"}\n' "$POGO_READ_ROOTS"
while [ "$#" -gt 0 ]; do
  case "$1" in
    -skill) shift; printf '{"type":"token","text":"skill=%s\\n"}\n' "$1" ;;
    -no-skills) printf '%s\n' '{"type":"token","text":"explicit-selection\n"}' ;;
  esac
  shift
done
printf '%s\n' '{"type":"run_end","stop_reason":"end_turn"}'
`
	if err := os.WriteFile(program, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		name := "ordinary"
		if explicit {
			name = "explicit"
		}
		t.Run(name, func(t *testing.T) {
			child := &tools.Subagent{
				Cwd: t.TempDir(), Exe: program,
				SkillPaths: view.pathFlags, SkillReadRoots: view.readRoots,
				ExplicitSkills: explicit,
			}
			result, err := child.Execute(context.Background(), json.RawMessage(`{"mode":"explore","wait":true,"task":"inspect inherited roots"}`))
			if err != nil {
				t.Fatalf("spawn child: %v", err)
			}
			wantRoots := operator
			if explicit {
				wantRoots = strings.Join(view.readRoots, string(os.PathListSeparator))
			}
			lines := strings.Split(result.Text, "\n")
			if !slices.Contains(lines, "roots="+wantRoots) || !slices.Contains(lines, "skill="+sk.Dir) {
				t.Errorf("child lost operator/skill inheritance: %q", result.Text)
			}
			if got := strings.Contains(result.Text, "explicit-selection"); got != explicit {
				t.Errorf("explicit discovery flag=%v, want %v: %q", got, explicit, result.Text)
			}
			if got := os.Getenv(tools.EnvReadRoots); got != operator {
				t.Errorf("parent read roots changed: bytes=%d, want operator-only bytes=%d", len(got), len(operator))
			}
		})
	}
}
