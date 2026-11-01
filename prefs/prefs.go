// Package prefs holds the browser-UI preferences that are not provider
// configuration: today, how long spilled tool output is kept.
//
// providers.json stays its own file (config/ owns it, with its strict
// decoder and its no-credentials rule); this is the small store beside it
// for everything the settings dialog edits that has nothing to do with
// models. The shape is deliberately one JSON object with flat fields, so a
// second preference later is a new field, not a new file.
//
// The load failure modes are split on purpose, because the spill sweeper
// reads this file before deleting anything:
//
//   - file missing → Default(), no error. A fresh machine gets the normal
//     retention without anyone writing a file first.
//   - file present but unreadable (bad JSON, an out-of-range value) → an
//     error. Falling back to Default() here would mean a hand-edit with a
//     typo silently restores the 7-day default and the next sweep deletes
//     what the operator asked to keep — the dangerous direction to be wrong
//     in. Callers that clean (the Manager's sweeper, -spill-prune) pause on
//     this error rather than guessing.
package prefs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvFile relocates the preferences file. Same shape as POGO_SPILL_DIR and
// friends: a deployment decides where state lives (a mounted /data volume
// keeps the setting across container rebuilds).
const EnvFile = "POGO_PREFS_FILE"

// DefaultSpillRetentionDays is how many days spilled output lives when nobody
// said otherwise.
const DefaultSpillRetentionDays = 7

// maxRetentionDays bounds the setting. Ten years is "forever" spelled out;
// beyond that the value is a typo, and treating a typo as forever is how a
// disk fills quietly.
const maxRetentionDays = 3650

// MaxTurnsBound bounds the MaxTurns override. The interactive default is
// already generous; far beyond it a typo is more likely than intent, and an
// unattended-looking cap is exactly what DefaultMaxTurns exists to prevent.
const MaxTurnsBound = 1000

// Prefs is the whole preferences file. Unknown fields in the file are
// tolerated on load (forward-compatible: an older binary must not choke on a
// newer one's settings); unknown fields in memory do not exist.
type Prefs struct {
	// SpillRetentionDays is how many days a spill file is kept. 0 disables
	// automatic cleanup entirely — the directory then grows until -spill-prune
	// with an explicit -spill-prune-days says otherwise.
	SpillRetentionDays int `json:"spill_retention_days"`
	// SandboxMode is the runtime override of the deployment's sandbox mode:
	// "", "enforce", "degrade" or "off". Empty means the POGO_SANDBOX env (or
	// its degrade default) decides — the pre-override behaviour. Set by the
	// settings dialog; applied at startup and changeable at runtime. It is a
	// deliberate, logged escape hatch, and the file itself is deny-bound in
	// the sandbox for exactly that reason (tools/sandbox.go homeDenyFiles).
	SandboxMode string `json:"sandbox_mode,omitempty"`
	// AttachmentExts is the runtime override of the attachment allowlist:
	// extensions (lowercase, dot-prefixed) the upload endpoints accept, drawn
	// from the groups web/attachments.go defines. Nil means the default set
	// (image + office + media groups). Set by the settings dialog; enforced by
	// saveAttachment so the client's door check and the server's gate read
	// one list and cannot drift apart.
	AttachmentExts []string `json:"attachment_exts,omitempty"`
	// AttachmentMaxMB is the runtime override of the per-file upload cap, in
	// megabytes. 0 means the built-in default (25). Clamped to 10–1024 on
	// use: below 10 the door refuses files the docs skill exists to read,
	// above 1024 the in-memory buffering per concurrent upload stops being a
	// bounded cost. Set by the settings dialog's slider.
	AttachmentMaxMB int `json:"attachment_max_mb,omitempty"`
	// MaxTurns is the runtime override of the interactive turn cap
	// (-max-turns, agent.DefaultMaxTurns). 0 means the startup flag decides —
	// the pre-override behaviour. Set by the settings dialog; the Manager
	// applies it to cached sessions so the next run of an already-open
	// conversation sees it without a reload. Schedule jobs keep their own
	// MaxJobTurns cap either way.
	MaxTurns int `json:"max_turns,omitempty"`
	// MemoryAuto turns on automatic memory distillation for interactive Studio
	// Agent sessions (web/memory_distill.go). False means memory stays
	// hand-written. Set by the settings dialog; the Manager reads the live
	// value at each run's end, so a change applies to the next run of an
	// already-open conversation without a reload.
	MemoryAuto bool `json:"memory_auto,omitempty"`
}

// Default is the configuration of a machine with no preferences file.
func Default() Prefs {
	return Prefs{SpillRetentionDays: DefaultSpillRetentionDays}
}

// Path resolves where the preferences live: $POGO_PREFS_FILE, else
// ~/.pogo/prefs.json beside sessions and the spill directory.
func Path() string {
	if p := strings.TrimSpace(os.Getenv(EnvFile)); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".pogo", "prefs.json")
	}
	return filepath.Join(os.TempDir(), "pogo", "prefs.json")
}

// Validate rejects the values that would make a sweep meaningless or
// dangerous. 0 is meaningful (disabled), so the bad direction is negative —
// which a prune would read as "older than the future", i.e. everything.
func (p Prefs) Validate() error {
	if p.SpillRetentionDays < 0 || p.SpillRetentionDays > maxRetentionDays {
		return fmt.Errorf("spill_retention_days must be 0 (disabled) or 1..%d, got %d",
			maxRetentionDays, p.SpillRetentionDays)
	}
	switch p.SandboxMode {
	case "", "enforce", "degrade", "off":
	default:
		return fmt.Errorf("sandbox_mode must be empty (follow env) or enforce|degrade|off, got %q", p.SandboxMode)
	}
	if p.MaxTurns < 0 || p.MaxTurns > MaxTurnsBound {
		return fmt.Errorf("max_turns must be 0 (follow -max-turns) or 1..%d, got %d",
			MaxTurnsBound, p.MaxTurns)
	}
	return nil
}

// Load reads and validates the preferences at path. A missing file is
// Default() with no error; a file that exists but cannot be parsed or fails
// Validate is an error — see the package comment for why the two cases must
// not share an answer.
func Load(path string) (Prefs, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return Prefs{}, err
	}
	var p Prefs
	// No DisallowUnknownFields: forward compatibility beats strictness here,
	// unlike providers.json, where an unknown field is a typo'd credential.
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&p); err != nil {
		return Prefs{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return Prefs{}, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// Save validates, then writes 0600 temp+rename like config.Save — the file
// may sit on a mounted volume in a container deployment, where the rename
// keeps a reader from ever meeting a half-written preference. The in-place
// fallback covers the odd single-file bind mount, same as config's.
func Save(path string, p Prefs) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if tmp, err := os.CreateTemp(filepath.Dir(path), "prefs.json.tmp-*"); err == nil {
		tmpName := tmp.Name()
		_ = tmp.Chmod(0o600)
		_, werr := tmp.Write(data)
		cerr := tmp.Close()
		if werr == nil && cerr == nil {
			if rerr := os.Rename(tmpName, path); rerr == nil {
				return nil
			}
			os.Remove(tmpName)
		} else {
			os.Remove(tmpName)
		}
		// Fall through to the in-place write: the bind-mount case.
	}

	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := out.Write(data); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
