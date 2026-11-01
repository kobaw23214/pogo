package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// A user message with a resolved image becomes the multimodal content array:
// the image part first, the text part after. The order is every provider's
// documented preference, and the array form is the only shape that carries an
// image at all.
func TestImageBlockBecomesContentParts(t *testing.T) {
	history := []Message{{Role: RoleUser, Content: []Block{
		{Type: BlockImage, ImageRef: "abc123", ImageMime: "image/png", DataURL: "data:image/png;base64,aGk="},
		{Type: BlockText, Text: "这张图里有什么？"},
	}}}

	wire := toWireMessages("", history)
	if len(wire) != 1 {
		t.Fatalf("got %d messages", len(wire))
	}
	if wire[0].Role != "user" || len(wire[0].Parts) != 2 {
		t.Fatalf("expected user message with 2 parts, got %+v", wire[0])
	}
	if wire[0].Parts[0].Type != "image_url" || wire[0].Parts[0].ImageURL == nil {
		t.Errorf("first part is not an image: %+v", wire[0].Parts[0])
	}
	if wire[0].Parts[0].ImageURL.URL != "data:image/png;base64,aGk=" {
		t.Errorf("image url: %q", wire[0].Parts[0].ImageURL.URL)
	}
	if wire[0].Parts[1].Type != "text" || wire[0].Parts[1].Text != "这张图里有什么？" {
		t.Errorf("second part is not the prompt: %+v", wire[0].Parts[1])
	}

	// The marshalled form must really be an array under "content" — the custom
	// MarshalJSON is what keeps Parts and Content from colliding.
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGk="}},{"type":"text","text":"这张图里有什么？"}]`) {
		t.Errorf("payload:\n%s", raw)
	}
}

// A text-only model receives the description instead of the pixels, as plain
// string content: sending the parts array to a provider without vision is a
// request error, not a graceful degradation.
func TestImageWithoutDataURLFallsBackToDescription(t *testing.T) {
	history := []Message{{Role: RoleUser, Content: []Block{
		{Type: BlockImage, ImageRef: "abc123", ImageName: "截图.png", ImageAlt: "一个蓝色按钮"},
		{Type: BlockText, Text: "复刻它"},
	}}}

	wire := toWireMessages("", history)
	if len(wire) != 1 {
		t.Fatalf("got %d messages", len(wire))
	}
	if wire[0].Parts != nil {
		t.Fatalf("text-only model must not receive parts: %+v", wire[0])
	}
	if !strings.Contains(wire[0].Content, "截图.png") || !strings.Contains(wire[0].Content, "一个蓝色按钮") {
		t.Errorf("description missing from fallback: %q", wire[0].Content)
	}
	if !strings.Contains(wire[0].Content, "复刻它") {
		t.Errorf("prompt text lost: %q", wire[0].Content)
	}
}

// An image with no description still yields a usable handle — the tool call is
// the recovery path, so the text must point at it.
func TestImageFallbackWithoutDescription(t *testing.T) {
	history := []Message{{Role: RoleUser, Content: []Block{
		{Type: BlockImage, ImageRef: "deadbeef"},
	}}}

	wire := toWireMessages("", history)
	if len(wire) != 1 || wire[0].Content == "" {
		t.Fatalf("expected non-empty fallback text, got %+v", wire)
	}
	if !strings.Contains(wire[0].Content, "analyze_image") {
		t.Errorf("fallback should point at the tool: %q", wire[0].Content)
	}
}

// An image-only message (no text at all) must still produce the parts array,
// without a trailing empty-text part.
func TestImageOnlyMessageHasNoEmptyTextPart(t *testing.T) {
	history := []Message{{Role: RoleUser, Content: []Block{
		{Type: BlockImage, ImageRef: "a", DataURL: "data:image/jpeg;base64,eA=="},
	}}}

	wire := toWireMessages("", history)
	if len(wire) != 1 || len(wire[0].Parts) != 1 || wire[0].Parts[0].Type != "image_url" {
		t.Fatalf("got %+v", wire)
	}
}

// The persisted form must not carry the bytes: DataURL is json:"-", so the
// session file holds only the reference. The round trip keeps the reference
// and the description the vision model wrote.
func TestImageBlockPersistsByReferenceOnly(t *testing.T) {
	in := Message{Role: RoleUser, Content: []Block{{
		Type: BlockImage, ImageRef: "abc123", ImageMime: "image/png",
		ImageName: "shot.png", ImageAlt: "一个表格", DataURL: "data:image/png;base64,aGk=",
	}}}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "base64") {
		t.Errorf("payload bytes leaked into the session form:\n%s", raw)
	}
	var out Message
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	b := out.Content[0]
	if b.ImageRef != "abc123" || b.ImageAlt != "一个表格" || b.ImageMime != "image/png" {
		t.Errorf("round trip lost fields: %+v", b)
	}
	if b.DataURL != "" {
		t.Errorf("DataURL should not survive a round trip: %q", b.DataURL)
	}
}

// The estimate follows what convert sends: 1600 for the bytes, the length of
// the description when the model reads text instead.
func TestEstimateTokensCountsImages(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: []Block{
		{Type: BlockImage, DataURL: "data:image/png;base64,x"},
		{Type: BlockImage, ImageAlt: "12345678"},
	}}}
	got := EstimateTokens(msgs)
	want := int64(1600+8) / BytesPerToken
	if got != want {
		t.Errorf("got %d want %d", got, want)
	}
}

// Details and Source are the two block fields that exist for interfaces and the
// transcript, never for the provider. Their staying off the wire is a property
// of toWireMessages building each wire struct field by field — this test is the
// alarm for the day someone replaces that with a whole-struct marshal.
func TestDetailsNeverReachTheWire(t *testing.T) {
	history := []Message{{Role: RoleUser, Content: []Block{{
		Type:      BlockToolResult,
		ToolUseID: "c1",
		Text:      "the output the model reads",
		Details:   json.RawMessage(`{"path":"x.go","marker":"details-marker"}`),
		Source:    json.RawMessage(`{"label":"S1","marker":"source-marker"}`),
	}}}}

	raw, err := json.Marshal(toWireMessages("", history))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"details-marker", "source-marker", `"details"`, `"source"`} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("%s reached the wire:\n%s", bad, raw)
		}
	}
	// The result itself must of course still be there.
	if !strings.Contains(string(raw), "the output the model reads") {
		t.Errorf("the result text went missing:\n%s", raw)
	}
}
