package agent

import (
	"encoding/json"
	"testing"
)

func TestBookmarksAreMetadataRatherThanWebSources(t *testing.T) {
	s := newSourceStore("")
	for _, action := range []string{"bookmarks", " bookmarks "} {
		args, _ := json.Marshal(map[string]any{"action": action})
		if src := s.Next("browser", map[string]any{"content_source": "pogo_bookmarks", "has_readable_content": false}, "saved", "saved titles and URLs", args); src != nil {
			t.Fatal("bookmark metadata manufactured a webpage citation")
		}
	}
	if len(s.All()) != 0 {
		t.Fatal("metadata changed source registry")
	}
	page := s.Next("browser", map[string]any{"url": "https://example.com/", "title": "Page", "has_readable_content": true}, "page", "observed content", json.RawMessage(`{"action":"snapshot"}`))
	if page == nil || page.Label != "S1" || page.Type != SourceWebPage {
		t.Fatal("actual page observations lost their citation")
	}
}
