package web

// Preferences and spill-directory API: GET/PUT /api/prefs and
// POST /api/prefs/prune-spill, backing the settings dialog's storage section.
//
// prefs/ owns the file and its failure semantics; this file only adapts them
// to HTTP. The one rule worth repeating because every handler obeys it: a
// preferences file that cannot be read never results in a delete — GET
// reports the error alongside the defaults, PUT replaces the file wholesale
// (which is also how a broken file gets fixed), and the prune endpoint
// refuses. Same trust bar as the rest of /api: the token holder can already
// run bash, which can delete the directory outright.

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/prefs"
	"github.com/yosukeno/pogo/tools"
)

// prefsPath is where this server's preferences live: the store the caller
// wired in serveWeb, with prefs.Path() as the same default the CLI resolves.
func (s *Server) prefsPath() string {
	if s.opts.PrefsStore != "" {
		return s.opts.PrefsStore
	}
	return prefs.Path()
}

// prefsView is what GET /api/prefs answers: the settings themselves, the
// spill directory's current cost, and the last sweep's outcome. load_error
// carries the "sweep is paused" reason when the file cannot be read — the
// dialog renders the section anyway (with the defaults to edit), because a
// broken file is precisely when the user needs the editor.
type prefsView struct {
	Prefs     prefs.Prefs  `json:"prefs"`
	Spill     spillUsage   `json:"spill"`
	Turns     maxTurnsView `json:"turns"`
	LastSweep *SpillSweep  `json:"last_sweep,omitempty"`
	LoadError string       `json:"load_error,omitempty"`
}

// maxTurnsView is the turn-cap state the dialog renders: the settings
// override (0 = none), the startup flag it falls back to, and the effective
// cap runs actually use right now.
type maxTurnsView struct {
	Override  int `json:"max_turns"`
	Flag      int `json:"flag"`
	Effective int `json:"effective"`
}

func (s *Server) maxTurnsSnapshot() maxTurnsView {
	flag := s.mgr.cfg.MaxTurns
	if flag <= 0 {
		flag = agent.DefaultMaxTurns
	}
	override := int(s.mgr.maxTurnsOverride.Load())
	effective := override
	if effective <= 0 {
		effective = flag
	}
	return maxTurnsView{Override: override, Flag: flag, Effective: effective}
}

type spillUsage struct {
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
	Dir   string `json:"dir"`
}

func (s *Server) prefsSnapshot() prefsView {
	var v prefsView
	p, err := prefs.Load(s.prefsPath())
	if err != nil {
		v.Prefs = prefs.Default()
		v.LoadError = err.Error()
	} else {
		v.Prefs = p
	}
	v.Spill.Dir = tools.DefaultSpillDir()
	files, bytes, uerr := tools.SpillDiskUsage(v.Spill.Dir)
	if uerr != nil {
		// Zero usage is what a fresh machine truthfully reports, so an
		// unreadable directory must not print the same numbers silently.
		v.LoadError = joinErr(v.LoadError, uerr.Error())
	} else {
		v.Spill.Files, v.Spill.Bytes = files, bytes
	}
	v.LastSweep = s.mgr.LastSpillSweep()
	v.Turns = s.maxTurnsSnapshot()
	return v
}

func joinErr(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

func (s *Server) handleGetPrefs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.prefsSnapshot())
}

// handlePutMaxTurns is the settings dialog's live turn cap; 0 restores the
// startup flag. It is its own endpoint for the same reason the sandbox switch
// is: PUT /api/prefs replaces the file wholesale, so a one-field save here
// must never travel through it. The live propagation happens first (the
// Manager pushes the cap into cached sessions), then the file is rewritten
// by load-modify-save so nothing else in prefs.json moves.
func (s *Server) handlePutMaxTurns(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MaxTurns int `json:"max_turns"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.MaxTurns < 0 || body.MaxTurns > prefs.MaxTurnsBound {
		writeErr(w, http.StatusBadRequest, fmt.Errorf(
			"max_turns must be 0 (follow -max-turns) or 1..%d, got %d", prefs.MaxTurnsBound, body.MaxTurns))
		return
	}
	s.mgr.SetMaxTurnsOverride(body.MaxTurns)
	p, err := prefs.Load(s.prefsPath())
	if err != nil {
		// Applied for this process, not persisted — the same contract the
		// sandbox switch reports, and for the same reason: the unreadable
		// file is a human's half-finished edit and must not be overwritten.
		writeErr(w, http.StatusConflict, fmt.Errorf(
			"turn cap applied for this run but prefs.json would not load (%v); the change was not persisted", err))
		return
	}
	p.MaxTurns = body.MaxTurns
	if err := prefs.Save(s.prefsPath(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if s.opts.Logger != nil {
		s.opts.Logger.Printf("max turns changed via settings dialog: %d (effective: %d)", body.MaxTurns, s.maxTurnsSnapshot().Effective)
	}
	writeJSON(w, http.StatusOK, s.maxTurnsSnapshot())
}

// handleSavePrefs replaces the preferences wholesale. The body is the prefs
// object itself; validation is prefs.Validate, the same rule the file load
// and the sweeper apply, so a value the dialog saves can never be one a
// later load refuses.
func (s *Server) handleSavePrefs(w http.ResponseWriter, r *http.Request) {
	var p prefs.Prefs
	if err := decodeBody(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := p.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := prefs.Save(s.prefsPath(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// The turn cap is the one preference live runs consume: push it into the
	// Manager so cached sessions pick it up before their next run, not after
	// the next restart. The memory switch needs no push either — sessions read
	// it live — but storing it keeps the Manager and the file in agreement for
	// anyone watching the value mid-process.
	s.mgr.SetMaxTurnsOverride(p.MaxTurns)
	s.mgr.SetMemoryAuto(p.MemoryAuto)
	if s.opts.Logger != nil {
		s.opts.Logger.Printf("preferences updated via settings dialog (spill retention: %d days, max turns: %d)", p.SpillRetentionDays, p.MaxTurns)
	}
	writeJSON(w, http.StatusOK, s.prefsSnapshot())
}

// handlePutMemoryAuto is the settings dialog's memory-distillation switch. It
// is its own endpoint for the same reason the turn cap's is: PUT /api/prefs
// replaces the file wholesale, so a one-field save here must never travel
// through it. The live value applies first (the next run's end reads it),
// then the file is rewritten by load-modify-save so nothing else in
// prefs.json moves.
func (s *Server) handlePutMemoryAuto(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MemoryAuto bool `json:"memory_auto"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.mgr.SetMemoryAuto(body.MemoryAuto)
	p, err := prefs.Load(s.prefsPath())
	if err != nil {
		// Applied for this process, not persisted — the same contract the
		// turn cap reports, and for the same reason: the unreadable file is a
		// human's half-finished edit and must not be overwritten.
		writeErr(w, http.StatusConflict, fmt.Errorf(
			"memory distillation switched for this run but prefs.json would not load (%v); the change was not persisted", err))
		return
	}
	p.MemoryAuto = body.MemoryAuto
	if err := prefs.Save(s.prefsPath(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if s.opts.Logger != nil {
		s.opts.Logger.Printf("memory auto-distillation changed via settings dialog: %t", body.MemoryAuto)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"memory_auto": s.mgr.MemoryAutoEnabled()})
}

// handlePruneSpill is the dialog's "prune now" button: one manual pass under
// the current retention. It shares Manager.pruneSpill with the background
// sweeper so the two can never disagree, and the pass counts as the last
// sweep the GET view reports.
func (s *Server) handlePruneSpill(w http.ResponseWriter, _ *http.Request) {
	res, err := s.mgr.pruneSpill(time.Now())
	switch {
	case errors.Is(err, errSpillPruneDisabled):
		writeErr(w, http.StatusBadRequest, err)
	case err != nil:
		// A preferences file that cannot be read pauses cleanup rather than
		// falling back to a default — the same rule the sweeper obeys.
		writeErr(w, http.StatusConflict, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"removed": res.Removed,
			"bytes":   res.Bytes,
		})
	}
}
