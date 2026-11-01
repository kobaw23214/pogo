package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/prefs"
	"github.com/yosukeno/pogo/tools"
)

// The command and the sweeper share the retention file and the prune
// mechanics; what stays command-specific — and so gets tested here — is the
// output contract and the two refusal rules.

// spillPruneEnv points the spill dir and the prefs file at temp dirs and
// plants one old and one fresh log, returning the spill dir.
func spillPruneEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(tools.SpillDirEnv, dir)
	t.Setenv(prefs.EnvFile, filepath.Join(t.TempDir(), "prefs.json"))
	now := time.Now()
	plant := func(name string, mtime time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	plant("bash-old0.log", now.Add(-30*24*time.Hour))
	plant("bash-new0.log", now)
	return dir
}

func TestSpillPruneCommandReportsWhatItDid(t *testing.T) {
	spillPruneEnv(t)
	if err := prefs.Save(prefs.Path(), prefs.Prefs{SpillRetentionDays: 7}); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := spillPruneCommand(&out, 0); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "removed 1 files") || !strings.Contains(got, "kept 1 files") ||
		!strings.Contains(got, "retention 7 days") {
		t.Errorf("output = %q, want removed/kept/retention all reported", got)
	}
}

// The override changes this run's window without writing the file back:
// -spill-prune-days is how a disabled or long retention gets a one-off pass.
func TestSpillPruneDaysOverridesWithoutSaving(t *testing.T) {
	spillPruneEnv(t)
	if err := prefs.Save(prefs.Path(), prefs.Prefs{SpillRetentionDays: 365}); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := spillPruneCommand(&out, 7); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "removed 1 files") {
		t.Errorf("output = %q, want the override to prune the 30-day-old file", out.String())
	}
	p, err := prefs.Load(prefs.Path())
	if err != nil || p.SpillRetentionDays != 365 {
		t.Errorf("prefs = %+v, %v; the override must not be written back", p, err)
	}
}

func TestSpillPruneCommandDisabledSaysSo(t *testing.T) {
	spillPruneEnv(t)
	if err := prefs.Save(prefs.Path(), prefs.Prefs{SpillRetentionDays: 0}); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := spillPruneCommand(&out, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "-spill-prune-days") {
		t.Errorf("output = %q, want the disabled message naming the override flag", out.String())
	}
}

// A prefs file that cannot be read stops the command with an error. The
// default retention is not a safe fallback for a delete operation.
func TestSpillPruneCommandRefusesBrokenPrefs(t *testing.T) {
	dir := spillPruneEnv(t)
	if err := os.WriteFile(prefs.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := spillPruneCommand(&out, 0); err == nil {
		t.Fatal("broken prefs must be an error, not a silent default")
	}
	if _, err := os.Stat(filepath.Join(dir, "bash-old0.log")); err != nil {
		t.Error("the old spill was deleted under broken prefs")
	}
}
