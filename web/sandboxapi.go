package web

// The sandbox's runtime switch: GET /api/sandbox shows the settings dialog the
// current containment, PUT /api/sandbox changes it without a restart.
//
// The switch is a deliberate, logged escape hatch — the user asked for it
// knowing the analysis (a boundary whose off-switch lives in a channel it
// guards). What keeps it honest rather than decorative: every change is
// persisted and logged with its direction, the UI shows what the mode costs,
// and the exemption that rests on the sandbox (readOnlyHint's auto-allow) is
// computed from the effective mode, so turning the boundary off brings the
// approval cards back instead of leaving a silent hole.

import (
	"fmt"
	"net/http"

	"github.com/yosukeno/pogo/prefs"
	"github.com/yosukeno/pogo/tools"
)

// sandboxView is GET /api/sandbox's payload: the full state the dialog needs
// to render the choice and its consequences, not just the current setting.
type sandboxView struct {
	// EnvMode is the deployment's baseline (POGO_SANDBOX, degrade by default).
	EnvMode string `json:"env_mode"`
	// Override is the runtime override, "" when none (the env decides).
	Override string `json:"override"`
	// Effective is what commands actually run under right now.
	Effective string `json:"effective"`
	// Active reports whether commands really run confined at this moment —
	// the fact the readOnlyHint exemption and the bash badge both rest on.
	Active bool `json:"active"`
	// Unavailable is why the sandbox cannot be used (empty when it can).
	Unavailable string `json:"unavailable,omitempty"`
	// Proc reports whether the probe got a private /proc.
	Proc bool `json:"proc"`
	// Net reports whether the sandbox allows network (POGO_SANDBOX_NET=1).
	Net bool `json:"net"`
}

func (s *Server) sandboxSnapshot() sandboxView {
	p := tools.InitSandbox()
	return sandboxView{
		EnvMode:     tools.SandboxPolicyFromEnv().Mode,
		Override:    tools.SandboxOverride(),
		Effective:   p.Mode,
		Active:      p.Active(),
		Unavailable: p.Unavailable,
		Proc:        p.Proc,
		Net:         p.Net,
	}
}

func (s *Server) handleGetSandbox(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.sandboxSnapshot())
}

// handlePutSandbox installs (or clears) the runtime override and persists it
// to prefs.json so the choice survives restarts. The prefs file is only
// rewritten when it loads cleanly: a corrupt one is a human's half-finished
// edit, and overwriting that silently would destroy the evidence.
func (s *Server) handlePutSandbox(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := tools.SetSandboxOverride(body.Mode); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	p, err := prefs.Load(s.prefsPath())
	if err != nil {
		// The override is already in effect for this process; not persisting
		// is reported, not hidden, and the file is left for the human to fix.
		writeErr(w, http.StatusConflict, fmt.Errorf(
			"override applied for this run but prefs.json would not load (%v); the change was not persisted", err))
		return
	}
	p.SandboxMode = body.Mode
	if err := prefs.Save(s.prefsPath(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if s.opts.Logger != nil {
		what := body.Mode
		if what == "" {
			what = "follow env"
		}
		s.opts.Logger.Printf("sandbox mode changed via settings dialog: %s (effective: %s)", what, s.sandboxSnapshot().Effective)
	}
	writeJSON(w, http.StatusOK, s.sandboxSnapshot())
}
