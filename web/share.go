package web

// Shared answers: the "share this answer" action on an exchange.
//
// A share is a snapshot, not a window. Creating one copies the question and
// the whole exchange — every turn's text, the tool calls and their results —
// out of the transcript into a small store; the link then serves that copy
// forever after, independent of the session — the conversation can continue,
// be renamed, even be deleted, and the page the recipient opened still says
// what it said when it was sent. Sharing a live view would instead mean the
// recipient's page silently changing, and the sharer having no real notion of
// what they had published.
//
// Access: the page is /share/{id} — a non-/api path, so it is public the way
// the SPA shell and the panels are (content, not operations). The data
// endpoint GET /api/share/{id} is the one API path exempted from the token
// gate for GET only — withToken holds that exemption. Creation stays
// token-gated: publishing is the owner's act. There is deliberately no
// revoke endpoint: the operator's answer to a link that should die is
// removing the entry from shares.json, not another API surface.

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/yosukeno/pogo/llm"
)

// shareLimits bound the store. The text cap mirrors what a transcript can
// honestly hold (answers are already tail-truncated at the tool layer; this
// catches a pathological one before it lands in a file read on every view).
// The snap cap bounds the whole timeline projection — messages plus tool
// results — that a share carries so the public page can render the work
// behind the answer. The count cap keeps shares.json from growing without
// bound on a server whose operator never looks at it — oldest out, the way a
// ring does it.
const (
	shareMaxText  = 2 << 20 // 2 MiB per question or answer
	shareMaxSnap  = 8 << 20 // 8 MiB for the exchange's messages + results
	shareMaxCount = 1000
)

// shareEntry is one published answer, in shares.json and on the wire. The
// same shape serves both so the dialog that creates a share and the page that
// renders it cannot disagree about fields.
type shareEntry struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	Model     string `json:"model,omitempty"`
	CreatedAt int64  `json:"created_at"`
	// SessionID is provenance for the owner ("which session was this from");
	// the snapshot itself never reads the session again.
	SessionID string `json:"session_id,omitempty"`
	Question  string `json:"question,omitempty"`
	Answer    string `json:"answer"`
	// Messages and Results are the exchange's timeline projection — the same
	// shape the session view and the subagent sheet render from — so the
	// public page shows the exchange exactly as the owner saw it: the work
	// log folded behind the conclusion, not just the answer's bare text.
	// Absent on shares created before the snapshot carried them; the page
	// falls back to the question/answer layout for those.
	Messages []Message             `json:"messages,omitempty"`
	Results  map[string]ToolResult `json:"results,omitempty"`
}

// shareStore is the shares.json persistence: a mutex, a path, the entries.
// Empty path means in-memory only (tests), same contract as the panel store.
type shareStore struct {
	mu      sync.RWMutex
	path    string
	entries map[string]shareEntry
	order   []string // creation order, for the size cap's eviction
}

func newShareStore(path string) (*shareStore, error) {
	s := &shareStore{path: path, entries: map[string]shareEntry{}}
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.entries); err != nil {
		return nil, err
	}
	for id, e := range s.entries {
		if e.ID == "" {
			e.ID = id
			s.entries[id] = e
		}
		s.order = append(s.order, id)
	}
	// File order is map iteration order, not creation order; sort by timestamp
	// so the eviction below removes the oldest share, not a random one.
	sort.SliceStable(s.order, func(i, j int) bool {
		return s.entries[s.order[i]].CreatedAt < s.entries[s.order[j]].CreatedAt
	})
	return s, nil
}

// save writes the store atomically (tmp + rename), the users.json pattern.
func (s *shareStore) save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *shareStore) get(id string) (shareEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[id]
	return e, ok
}

func (s *shareStore) removeSessions(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	remove := make(map[string]bool, len(ids))
	for _, id := range ids {
		remove[id] = true
	}
	for id, e := range s.entries {
		if remove[e.SessionID] {
			delete(s.entries, id)
		}
	}
	order := s.order[:0]
	for _, id := range s.order {
		if _, ok := s.entries[id]; ok {
			order = append(order, id)
		}
	}
	s.order = order
	return s.save()
}

// put stores an entry and persists, evicting past the count cap.
func (s *shareStore) put(e shareEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[e.ID]; !exists {
		s.order = append(s.order, e.ID)
	}
	s.entries[e.ID] = e
	for len(s.order) > shareMaxCount {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.entries, oldest)
	}
	return s.save()
}

// handleShareCreate snapshots one exchange out of a session. The client sends
// the question's timeline id — the hub's u<N>, the same id a rewind targets —
// and the ordinal mapping locates the records: the hub counts visible user
// messages, the store counts them by the same rule (Store.ExchangeSpan), so
// what gets published is exactly the exchange the sharer saw, all its turns'
// texts included, without the server re-implementing the exchange fold.
func (s *Server) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	if s.rejectBackgroundTaskWrite(w, sess.ID) {
		return
	}
	var body struct {
		MessageID string `json:"message_id"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.MessageID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("message_id is required"))
		return
	}

	k, ok := sess.hub.UserMessageOrdinal(body.MessageID)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("message not found on the current branch"))
		return
	}
	sess.mu.Lock()
	question, answer, found := sess.store.ExchangeSpan(k)
	span, _ := sess.store.ExchangeMessages(k)
	sess.mu.Unlock()
	if !found {
		// The hub and the store count by the same rule, so a miss here is drift
		// between the two, not user error — but the answer is the same either
		// way: no snapshot.
		writeErr(w, http.StatusNotFound, errors.New("message not found on the current branch"))
		return
	}
	if answer == "" {
		writeErr(w, http.StatusBadRequest, errors.New("that exchange has no answer text to share"))
		return
	}
	if len(answer) > shareMaxText || len(question) > shareMaxText {
		writeErr(w, http.StatusRequestEntityTooLarge,
			errors.New("answer or question exceeds the share size cap"))
		return
	}

	// Attachments on the question are references into the owner's workspace,
	// which an anonymous reader can neither open nor make sense of — the text
	// is the question. Strip them before the projection, not after, so the
	// folded view never shows a card that goes nowhere. Fresh slice: the span
	// shares its block backing arrays with the store.
	q := span[0].Message
	var textOnly []llm.Block
	for _, b := range q.Content {
		if b.Type == llm.BlockText {
			textOnly = append(textOnly, b)
		}
	}
	span[0].Message.Content = textOnly

	// Run the span through a fresh hub so the snapshot speaks exactly the
	// session view's dialect: message ids, the results keyed by call id, the
	// works. The page then renders with the same components, and the two can
	// never drift apart over what a turn looks like.
	proj := NewHub()
	proj.Seed(span)
	snap := proj.Snapshot()

	packed, err := json.Marshal(struct {
		M []Message             `json:"m"`
		R map[string]ToolResult `json:"r"`
	}{snap.Messages, snap.Results})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if len(packed) > shareMaxSnap {
		writeErr(w, http.StatusRequestEntityTooLarge,
			errors.New("the exchange's timeline exceeds the share size cap"))
		return
	}

	id := sess.ID
	var title, model string
	if list, err := s.mgr.List(); err == nil {
		for _, info := range list {
			if info.ID == id {
				title, model = info.Title, info.Model
				break
			}
		}
	}

	entry := shareEntry{
		ID:        randomID(16),
		Title:     title,
		Model:     model,
		CreatedAt: time.Now().UnixMilli(),
		SessionID: id,
		Question:  question,
		Answer:    answer,
		Messages:  snap.Messages,
		Results:   snap.Results,
	}
	if err := s.shares.put(entry); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         entry.ID,
		"url":        "/share/" + entry.ID,
		"created_at": entry.CreatedAt,
	})
}

// handleShareView serves one snapshot. Public: withToken exempts GETs under
// /api/share/ — this handler answers nothing else, so the exemption's reach
// is this page's data and nothing more.
func (s *Server) handleShareView(w http.ResponseWriter, r *http.Request) {
	e, ok := s.shares.get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, e)
}
