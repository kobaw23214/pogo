package prefs

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The split that matters: a missing file means defaults, a broken file means
// stop. The sweeper deletes files on this package's say-so, so the two
// answers can never be allowed to blur.

func TestLoadMissingFileReturnsDefault(t *testing.T) {
	p, err := Load(filepath.Join(t.TempDir(), "prefs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, Default()) || p.SpillRetentionDays != DefaultSpillRetentionDays {
		t.Errorf("got %+v, want the default %+v", p, Default())
	}
}

func TestLoadBrokenFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a file that cannot be parsed must not silently become the default")
	}
}

// An out-of-range retention is the same kind of broken as bad JSON: loading
// it as-is would hand the sweeper a nonsense window.
func TestLoadInvalidValueIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	if err := os.WriteFile(path, []byte(`{"spill_retention_days": -3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a negative retention must fail validation")
	}
}

// The turn cap accepts 0 (follow the flag) and the documented bound, and
// refuses both directions outside it: a negative or huge cap is a typo, not
// a budget anyone asked for.
func TestValidateMaxTurns(t *testing.T) {
	for _, good := range []int{0, 1, 300, MaxTurnsBound} {
		if err := (Prefs{MaxTurns: good}).Validate(); err != nil {
			t.Errorf("MaxTurns %d should validate, got %v", good, err)
		}
	}
	for _, bad := range []int{-1, MaxTurnsBound + 1} {
		if err := (Prefs{MaxTurns: bad}).Validate(); err == nil {
			t.Errorf("MaxTurns %d must fail validation", bad)
		}
	}
}

// Unknown fields pass through the decoder untouched: a file written by a
// newer pogo must still load under this one.
func TestLoadToleratesUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	if err := os.WriteFile(path, []byte(`{"spill_retention_days": 30, "future_knob": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.SpillRetentionDays != 30 {
		t.Errorf("got %d days, want 30", p.SpillRetentionDays)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "prefs.json")
	if err := Save(path, Prefs{SpillRetentionDays: 0}); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.SpillRetentionDays != 0 {
		t.Errorf("got %d, want 0 (disabled is a value worth keeping)", p.SpillRetentionDays)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perms = %o, want 600", perm)
	}
}

func TestSaveRefusesInvalidValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.json")
	if err := Save(path, Prefs{SpillRetentionDays: 3651}); err == nil {
		t.Fatal("Save must not persist a value Validate rejects")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the invalid value was written anyway")
	}
}

func TestPathHonoursTheEnvOverride(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "elsewhere.json")
	t.Setenv(EnvFile, custom)
	if got := Path(); got != custom {
		t.Errorf("Path() = %q, want the %s override %q", got, EnvFile, custom)
	}
	// Without the override the file lives next to the rest of ~/.pogo.
	t.Setenv(EnvFile, "")
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory in this environment")
	}
	if got := Path(); !strings.HasPrefix(got, filepath.Join(home, ".pogo")) {
		t.Errorf("Path() = %q, want under ~/.pogo", got)
	}
}
