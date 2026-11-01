package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The search endpoint is the index's public face: it must find a session by a
// word that appears only inside the conversation, and refuse an empty query
// rather than return everything (which reads as "all sessions match" in a UI).
func TestSearchEndpointFindsSessionByText(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("quokka facts incoming")))
	sid := h.createSession()
	s := h.start(sid, "tell me about the quokka")
	s.wait(t, EvRunEnd)
	s.close()

	resp := h.do(http.MethodGet, "/api/sessions/search?q=quokka&limit=5", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search: %d", resp.StatusCode)
	}
	var out struct {
		Q    string `json:"q"`
		Hits []struct {
			ID     string `json:"id"`
			Blocks []struct {
				Text   string `json:"text"`
				Anchor string `json:"anchor"`
			} `json:"blocks"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Hits) != 1 || out.Hits[0].ID != sid {
		t.Fatalf("hits = %+v, want exactly session %s", out.Hits, sid)
	}
	// The hit must quote the conversation around the match — the sidebar
	// renders these blocks as the "what matched" lines under the title, each
	// clickable through to the message holding the anchor.
	if len(out.Hits[0].Blocks) == 0 || !strings.Contains(out.Hits[0].Blocks[0].Text, "quokka") {
		t.Fatalf("blocks = %+v, want one quoting the match", out.Hits[0].Blocks)
	}

	// Empty q is a client mistake; answering it with the full listing would
	// render as "everything matched".
	bad := h.do(http.MethodGet, "/api/sessions/search?q="+url.PathEscape("  "), "")
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("blank q: %d, want 400", bad.StatusCode)
	}
}
