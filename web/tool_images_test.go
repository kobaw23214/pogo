package web

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

func TestToolImageReferencesSurviveSessionReloadAndCollection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	transcript := "{\"type\":\"meta\",\"meta\":{\"cwd\":\"/workspace\"}}\n" +
		"{\"type\":\"message\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"shot\",\"images\":[{\"type\":\"image\",\"image_ref\":\"uploads/browser.jpg\",\"image_name\":\"browser.jpg\"}]}]}}\n"
	if err := os.WriteFile(path, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	refs, cwd := sessionUploadRefs(path)
	if cwd != "/workspace" || len(refs) != 1 || refs[0] != "uploads/browser.jpg" {
		t.Fatalf("GC missed screenshot reference: cwd=%s refs=%v", cwd, refs)
	}
	s := &Session{}
	s.seedImages([]session.Timed{{Message: llm.Message{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult,
		Images: []llm.Block{{Type: llm.BlockImage, ImageRef: refs[0], ImageName: "browser.jpg"}},
	}}}}})
	images := s.recentImages()
	if len(images) != 1 || images[0].Ref != refs[0] {
		t.Fatalf("reloaded screenshot unavailable to analyze_image: %+v", images)
	}
}
