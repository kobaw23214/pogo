package web

// The attachment allowlist's runtime switch, mirroring sandboxapi.go: GET
// /api/attachments/policy shows the settings dialog the groups and the
// effective list; PUT saves an override to prefs.json without a restart.
//
// The point of the endpoint is that there is one list. The composer's door
// check, the file picker's accept attribute and saveAttachment's gate all
// read what this returns, so "what the UI offers" and "what the server
// keeps" cannot drift apart — the client fetches instead of hardcoding.

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/yosukeno/pogo/prefs"
)

// attachmentPolicyView is GET's payload: the groups (for the dialog's
// checkboxes), the default, the override (nil = follow default), the
// effective list (for the composer's door check and accept attribute), and
// the size cap the door also mirrors — same three-value shape as the
// allowlist (default / override / effective).
type attachmentPolicyView struct {
	Groups      []AttachGroup `json:"groups"`
	DefaultExts []string      `json:"default_exts"`
	Override    []string      `json:"override,omitempty"`
	AllowedExts []string      `json:"allowed_exts"`
	MaxMB       int           `json:"max_mb"`
	MaxMBOver   int           `json:"max_mb_override,omitempty"`
	MinMB       int           `json:"min_mb"`
	CapMB       int           `json:"cap_mb"`
}

func (s *Server) attachmentPolicySnapshot() attachmentPolicyView {
	p := prefs.Default()
	override := []string(nil)
	if loaded, err := prefs.Load(s.prefsPath()); err == nil {
		p = loaded
		// Normalised copy so the dialog never sees "jpg" and ".jpg" as two
		// states of the same choice.
		override = normalizeExts(loaded.AttachmentExts)
	}
	effective := effectiveAttachExts(p.AttachmentExts)
	return attachmentPolicyView{
		Groups:      attachGroups,
		DefaultExts: defaultAttachExts(),
		Override:    override,
		AllowedExts: sortedExts(effective),
		MaxMB:       effectiveAttachMaxMB(p.AttachmentMaxMB),
		MaxMBOver:   p.AttachmentMaxMB,
		MinMB:       attachMinMB,
		CapMB:       attachMaxMB,
	}
}

func (s *Server) handleGetAttachmentPolicy(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.attachmentPolicySnapshot())
}

// handlePutAttachmentPolicy saves a new allowlist. Extensions outside the
// universe are refused with the reason: the settings dialog only offers
// group toggles, so an unknown extension means a hand-edited prefs.json or
// a stale dialog, and silently dropping it would save a different policy
// than the one was reviewed. Same corrupt-file rule as the sandbox PUT: the
// file is only rewritten when it loads cleanly.
func (s *Server) handlePutAttachmentPolicy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Exts  []string `json:"exts"`
		MaxMB *int     `json:"max_mb"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// The slider's bounds are the server's, and the server says so: a value
	// outside 10–1024 is a stale dialog or a hand-crafted PUT, and clamping
	// it silently would save a different cap than the one was reviewed.
	if body.MaxMB != nil && (*body.MaxMB != 0 &&
		(*body.MaxMB < attachMinMB || *body.MaxMB > attachMaxMB)) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf(
			"单文件上限须在 %d–%d MB 之间（0 = 默认 %d MB）",
			attachMinMB, attachMaxMB, attachMaxBytes>>20))
		return
	}
	exts := normalizeExts(body.Exts)
	universe := attachUniverse()
	for _, e := range exts {
		if !universe[e] {
			writeErr(w, http.StatusBadRequest, fmt.Errorf(
				"不支持的扩展名 %q（可选范围：%s）", e, strings.Join(sortedExts(universe), " ")))
			return
		}
	}
	p, err := prefs.Load(s.prefsPath())
	if err != nil {
		writeErr(w, http.StatusConflict, fmt.Errorf(
			"prefs.json 无法读取（%v），未保存更改", err))
		return
	}
	if len(exts) == 0 {
		// An empty selection is "follow the default", not "allow nothing":
		// allow-nothing makes every upload fail at the door with a message
		// nobody reads twice, and unchecking every group in the dialog is
		// how it would happen by accident.
		p.AttachmentExts = nil
	} else {
		p.AttachmentExts = exts
	}
	if body.MaxMB != nil {
		p.AttachmentMaxMB = *body.MaxMB // 0 = follow the default
	}
	if err := prefs.Save(s.prefsPath(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if s.opts.Logger != nil {
		s.opts.Logger.Printf("attachment policy changed via settings dialog: %d extensions (default has %d), cap %d MB",
			len(exts), len(defaultAttachExts()), effectiveAttachMaxMB(p.AttachmentMaxMB))
	}
	writeJSON(w, http.StatusOK, s.attachmentPolicySnapshot())
}
