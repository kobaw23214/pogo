package agent

import (
	"context"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// RunMessage + ImageResolver: a first message carrying an image block reaches
// the model with the data URL the resolver produced, while the agent's own
// transcript (Messages) keeps the reference form — the session file must never
// grow the base64 payload.
func TestRunMessageResolvesImagesForTheWireOnly(t *testing.T) {
	client := &fakeClient{}
	a := New(Config{
		Client: client,
		Registry: tools.NewRegistry(),
		ImageResolver: func(ref string) (string, bool) {
			if ref == "img1" {
				return "data:image/png;base64,aGk=", true
			}
			return "", false
		},
	})

	first := llm.Message{Role: llm.RoleUser, Content: []llm.Block{
		{Type: llm.BlockImage, ImageRef: "img1", ImageAlt: "一张截图"},
		{Type: llm.BlockText, Text: "看看这个"},
	}}
	for range drain(a.RunMessage(context.Background(), first)) {
	}

	seen := client.lastSeen()
	if len(seen) == 0 || len(seen[0].Content) != 2 {
		t.Fatalf("model history = %+v", seen)
	}
	if seen[0].Content[0].DataURL != "data:image/png;base64,aGk=" {
		t.Errorf("image not resolved on the wire: %+v", seen[0].Content[0])
	}

	// The transcript keeps the reference: no bytes in Messages.
	msgs := a.Messages()
	if msgs[0].Content[0].DataURL != "" || msgs[0].Content[0].ImageRef != "img1" {
		t.Errorf("transcript must keep the reference form: %+v", msgs[0].Content[0])
	}
}

// A resolver that says no leaves the block unresolved, which is convert.go's
// cue to send the description instead — the model-switch story, where the same
// history is served differently depending on who is asking.
func TestResolveImagesLeavesUnresolvableBlocksAlone(t *testing.T) {
	msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{
		{Type: llm.BlockImage, ImageRef: "gone"},
		{Type: llm.BlockText, Text: "hi"},
	}}}
	a := New(Config{
		Client:   &fakeClient{},
		Registry: tools.NewRegistry(),
		ImageResolver: func(ref string) (string, bool) {
			return "", false // the store no longer has it, or the model sees text
		},
	})
	out := a.resolveImages(msgs)
	if &out[0] == &msgs[0] {
		t.Fatal("expected a copied view for a message with an image")
	}
	if out[0].Content[0].DataURL != "" {
		t.Errorf("unresolvable image must stay unresolved: %+v", out[0].Content[0])
	}
	// The text block is shared, not copied — only image-bearing messages pay.
	if len(out) != 1 || out[0].Content[1].Text != "hi" {
		t.Errorf("text block changed: %+v", out[0].Content[1])
	}
}

// No image blocks, no copies: the common conversation must not allocate for
// this feature.
func TestResolveImagesReturnsInputWhenNothingToResolve(t *testing.T) {
	msgs := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{
		{Type: llm.BlockText, Text: "plain"},
	}}}
	a := New(Config{
		Client:        &fakeClient{},
		Registry:      tools.NewRegistry(),
		ImageResolver: func(string) (string, bool) { return "data:,x", true },
	})
	if got := a.resolveImages(msgs); &got[0] != &msgs[0] {
		t.Error("expected the input slice back unchanged")
	}
}

// Run stays the text-only entry point it always was, delegating to RunMessage.
func TestRunStillAcceptsPlainText(t *testing.T) {
	client := &fakeClient{}
	a := New(Config{Client: client, Registry: tools.NewRegistry()})
	for range drain(a.Run(context.Background(), "hello")) {
	}
	seen := client.lastSeen()
	if len(seen) == 0 || seen[0].Content[0].Type != llm.BlockText || seen[0].Content[0].Text != "hello" {
		t.Fatalf("history = %+v", seen)
	}
}
