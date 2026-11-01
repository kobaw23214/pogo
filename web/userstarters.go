package web

// User starters: the empty state's cards, editable from the browser.
//
// The skill-provided starters.json stays the default (see skills/starters.go
// for why the domain belongs there). This file adds the override layer: a
// user-authored set that, once saved, wins entirely. The first edit seeds the
// override with the currently effective set, so fixing one card does not
// discard the rest, and followups pass through untouched — the editor only
// knows cards.
//
// Same trust bar as the rest of /api: editing the empty state is far less
// privilege than the bash tool the same token already drives.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/yosukeno/pogo/skills"
)

// startersMaxBody caps the PUT: six cards with 2KB prompts is ~13KB; a
// megabyte is generous and still cheap to refuse.
const startersMaxBody = 1 << 20

var startersWriteMu sync.Mutex

// userStarters loads the override set. Nil means "no override" (skill
// defaults apply) — a missing file is the normal state, not an error.
func (s *Server) userStarters() (*skills.Starters, []string) {
	if s.opts.StartersStore == "" {
		return nil, nil
	}
	data, err := os.ReadFile(s.opts.StartersStore)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, []string{err.Error()}
		}
		return nil, nil
	}
	st, msgs := skills.ParseUserStarters(data)
	return st, msgs
}

// handleSaveStarters validates and persists the edited set. The body is the
// same shape GET serves; the client echoes followups through, and they get
// the same validation the skill files do.
func (s *Server) handleSaveStarters(w http.ResponseWriter, r *http.Request) {
	startersWriteMu.Lock()
	defer startersWriteMu.Unlock()
	if s.opts.StartersStore == "" {
		writeErr(w, http.StatusInternalServerError, errors.New("no starters store configured"))
		return
	}
	body := make([]byte, 0, 4096)
	buf := make([]byte, 32*1024)
	for total := 0; ; {
		n, err := r.Body.Read(buf)
		total += n
		if total > startersMaxBody {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("body exceeds %d bytes", startersMaxBody))
			return
		}
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if err != nil {
			break
		}
	}

	var version struct {
		ExpectedRevision *string `json:"expected_revision"`
	}
	if json.Unmarshal(body, &version) != nil {
		writeErr(w, 400, errors.New("无效的快捷卡片数据"))
		return
	}
	if version.ExpectedRevision != nil && *version.ExpectedRevision != s.effectiveStarters().Revision {
		writeErr(w, http.StatusConflict, errors.New("首页快捷卡片已在其他窗口更新，请重新打开后再编辑"))
		return
	}
	// The revision belongs to this write request, not the persisted starter
	// schema. Remove only that envelope field; retain strict validation of all
	// actual starter fields, including unknown ones.
	if version.ExpectedRevision != nil {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("无效的快捷卡片数据"))
			return
		}
		delete(fields, "expected_revision")
		body, _ = json.Marshal(fields)
	}
	st, msgs := skills.ParseUserStarters(body)
	if st == nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf(
			"invalid starter set: %v", msgs))
		return
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if dir := filepath.Dir(s.opts.StartersStore); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	// Temp+rename like config.Save. The store sits in a mounted directory in
	// container deployments (POGO_STARTERS_FILE under /data), where renames
	// are fine; the in-place fallback covers the odd single-file mount.
	tmp, err := os.CreateTemp(filepath.Dir(s.opts.StartersStore), "starters.json.tmp-*")
	if err == nil {
		if _, werr := tmp.Write(append(data, '\n')); werr == nil && tmp.Close() == nil {
			if rerr := os.Rename(tmp.Name(), s.opts.StartersStore); rerr == nil {
				s.finishStartersSave(w, st, msgs)
				return
			}
			os.Remove(tmp.Name())
		} else {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}
	out, err2 := os.OpenFile(s.opts.StartersStore, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err2 != nil {
		writeErr(w, http.StatusInternalServerError, err2)
		return
	}
	if _, err := out.Write(data); err != nil {
		out.Close()
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out.Close()
	s.finishStartersSave(w, st, msgs)
}

func (s *Server) finishStartersSave(w http.ResponseWriter, st *skills.Starters, msgs []string) {
	if s.opts.Logger != nil {
		s.opts.Logger.Printf("starter set updated via editor (%d cards)", len(st.Cards))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"cards":    len(st.Cards),
		"warnings": msgs,
	})
}
