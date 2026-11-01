package web

// Homepage cards are a live projection, never copied into the private shortcut
// document. Editing or deleting the original therefore cannot leave a stale
// duplicate behind. Their content hash is also the selection version.
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/skills"
)

type effectiveStarterSet struct {
	Revision  string                 `json:"revision,omitempty"`
	Heading   string                 `json:"heading,omitempty"`
	Send      bool                   `json:"send,omitempty"`
	Cards     []skills.StarterCard   `json:"cards"`
	Followups []skills.FollowupGroup `json:"followups"`
	Custom    bool                   `json:"custom,omitempty"`
}

func starterSetRevision(set effectiveStarterSet) string {
	set.Revision = ""
	raw, _ := json.Marshal(set)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Server) shortcutStarterCards(agentID string) []skills.StarterCard {
	if agentID == "" {
		return s.effectiveStarters().Cards
	}
	a, ok := s.mgr.studio.get(agentID)
	if !ok {
		return nil
	}
	cards := make([]skills.StarterCard, 0, len(a.Starters))
	for _, entry := range a.Starters {
		title := strings.TrimSpace(entry.Title)
		if title == "" {
			title = strings.TrimSpace(entry.Prompt)
		}
		cards = append(cards, skills.StarterCard{Title: title, Label: entry.Label, Prompt: entry.Prompt, Icon: entry.Icon, Parts: inputref.Clone(entry.Parts), InputLayout: entry.InputLayout})
	}
	return cards
}

func starterShortcutID(agentID string, card skills.StarterCard, occurrence int) string {
	raw, _ := json.Marshal(struct {
		Agent      string
		Card       skills.StarterCard
		Occurrence int
	}{agentID, card, occurrence})
	sum := sha256.Sum256(raw)
	return "starter_" + hex.EncodeToString(sum[:])
}

func (s *Server) starterShortcutProjection(agentID string) ([]userShortcut, []skills.StarterCard) {
	items, actions := []userShortcut{}, []skills.StarterCard{}
	seen := map[string]int{}
	for _, card := range s.shortcutStarterCards(agentID) {
		if card.Panel != "" {
			actions = append(actions, card)
			continue
		}
		if strings.TrimSpace(card.Prompt) == "" && len(card.Parts) == 0 {
			continue
		}
		key := starterShortcutID(agentID, card, 0)
		index := seen[key]
		seen[key]++
		title := []rune(card.Title)
		// Legacy plain-string starters use their entire prompt as the title.
		// Only the chip label is shortened; the exact prompt remains intact.
		if len(title) > 120 {
			title = title[:120]
		}
		items = append(items, userShortcut{ID: starterShortcutID(agentID, card, index), Title: string(title), Content: card.Prompt, Icon: card.Icon, Parts: inputref.Clone(card.Parts), InputLayout: card.InputLayout})
	}
	return items, actions
}

func (s *Server) projectStarterShortcuts(view *shortcutView, r *http.Request) {
	view.StarterShortcuts, view.StarterActions = s.starterShortcutProjection(view.AgentID)
	// This projection is a read-only source inside the private shortcut tree.
	// Homepage/Studio management keeps its own existing authorization routes.
	view.CanManageStarters = false
}

// A caller supplies only a currently visible content-addressed source ID. The
// server supplies the immutable source payload and uses the existing published
// starter resolver, including its private-nested-reference isolation. Arbitrary
// public asset handles are never accepted through the private shortcut API.
func (s *Server) handleStarterShortcutResolve(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	if sess.studioID != "" {
		a, visible := s.mgr.studio.get(sess.studioID)
		if !visible || a.Status != "ready" && !s.adminRequest(r) {
			writeErr(w, 404, errors.New("Agent 不存在或不可访问"))
			return
		}
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2))
	if err != nil || len(bytes.TrimSpace(raw)) != 0 {
		writeErr(w, 400, errors.New("快捷卡片引用只接受当前来源标识"))
		return
	}
	items, _ := s.starterShortcutProjection(sess.studioID)
	for _, item := range items {
		if item.ID != r.PathValue("starter") {
			continue
		}
		parts := inputref.Clone(item.Parts)
		if len(parts) == 0 {
			parts = []MessageInputPart{{Type: "text", Text: item.Content}}
		}
		body, _ := json.Marshal(starterAssetInput{Prompt: item.Content, Parts: parts, InputLayout: item.InputLayout})
		forward := r.Clone(r.Context())
		forward.Body = io.NopCloser(bytes.NewReader(body))
		s.savedInputAssets(w, forward, true, false)
		return
	}
	writeErr(w, http.StatusConflict, errors.New("首页快捷卡片已更新或删除，请刷新列表后重新选择"))
}
