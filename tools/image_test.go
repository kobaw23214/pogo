package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func fixedAsk(answer string) func(context.Context, string, string) (string, error) {
	return func(_ context.Context, ref, question string) (string, error) {
		return answer + " [" + ref + "/" + question + "]", nil
	}
}

// The happy path: an explicit name resolves to its ref, the question rides
// through, and the answer reaches the model as text.
func TestImageAnalyzeAnswersByName(t *testing.T) {
	tool := &ImageAnalyze{
		Ask: fixedAsk("a blue button"),
		Recent: func() []ImageHandle {
			return []ImageHandle{{Ref: "aaa", Name: "shot.png"}, {Ref: "bbb", Name: "chart.png"}}
		},
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(
		`{"image":"chart.png","question":"图里几根柱子？"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "a blue button") || !strings.Contains(res.Text, "bbb") {
		t.Errorf("answer = %q", res.Text)
	}
}

// Omitting the image asks about the most recent attachment — the common case
// is one screenshot and a question about it.
func TestImageAnalyzeDefaultsToLatest(t *testing.T) {
	var askedRef string
	tool := &ImageAnalyze{
		Ask: func(_ context.Context, ref, _ string) (string, error) {
			askedRef = ref
			return "ok", nil
		},
		Recent: func() []ImageHandle {
			return []ImageHandle{{Ref: "aaa", Name: "old.png"}, {Ref: "bbb", Name: "new.png"}}
		},
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"what"}`)); err != nil {
		t.Fatal(err)
	}
	if askedRef != "bbb" {
		t.Errorf("asked about %q, want the latest bbb", askedRef)
	}
	// The explicit spelling of the same thing.
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"image":"latest","question":"what"}`)); err != nil {
		t.Fatal(err)
	}
	if askedRef != "bbb" {
		t.Errorf("asked about %q, want bbb", askedRef)
	}
}

func TestImageAnalyzeAcceptsWorkspacePathWithoutAttachments(t *testing.T) {
	tool := &ImageAnalyze{Ask: fixedAsk("autumn forest")}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"image":"downloads/autumn.png","question":"what is shown?"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "autumn forest") || !strings.Contains(res.Text, "downloads/autumn.png") {
		t.Errorf("workspace path did not reach the vision helper: %q", res.Text)
	}
}

// A name the model half-remembers still resolves by suffix, because the note
// quotes the original filename and models drop directories and counters.
func TestImageAnalyzeResolvesNameSuffix(t *testing.T) {
	tool := &ImageAnalyze{
		Ask: fixedAsk("x"),
		Recent: func() []ImageHandle {
			return []ImageHandle{{Ref: "abc", Name: "Downloads/屏幕截图 2026-08-21.png"}}
		},
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(
		`{"image":"屏幕截图 2026-08-21.png","question":"q"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "abc") {
		t.Errorf("suffix name did not resolve: %q", res.Text)
	}
}

// An unavailable vision helper is a text result, not an error: the loop would
// turn an error into a failed tool result anyway, and the wording is ours to
// keep helpful ("retry" is actionable, "exit 1" is not).
func TestImageAnalyzeReportsHelperFailureAsText(t *testing.T) {
	tool := &ImageAnalyze{
		Ask: func(context.Context, string, string) (string, error) {
			return "", context.DeadlineExceeded
		},
		Recent: func() []ImageHandle { return []ImageHandle{{Ref: "a", Name: "n"}} },
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"q"}`))
	if err != nil {
		t.Fatalf("helper failure must not be a tool error: %v", err)
	}
	if !strings.Contains(res.Text, "vision model unavailable") {
		t.Errorf("text = %q", res.Text)
	}
}

// No question, no attachments: both are refusals the model can act on.
func TestImageAnalyzeRefusesEmptyQuestionAndUnknownImage(t *testing.T) {
	tool := &ImageAnalyze{Ask: fixedAsk("x")}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"  "}`)); err == nil {
		t.Error("empty question accepted")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"image":"nope","question":"q"}`)); err == nil {
		t.Error("unknown image accepted")
	}
}

// The registry wires the tool under Options.Images, and a nil Images leaves it
// absent — the structural rule every capability grant follows.
func TestImageAnalyzeRegistration(t *testing.T) {
	with := New(Options{Images: &ImageAnalyze{Ask: fixedAsk("x")}})
	if _, ok := with.Get("analyze_image"); !ok {
		t.Error("analyze_image not registered")
	}
	without := New(Options{})
	if _, ok := without.Get("analyze_image"); ok {
		t.Error("analyze_image registered without an Images option")
	}
	// Read-only sessions keep it: asking about a picture changes nothing.
	ro := New(Options{ReadOnly: true, Images: &ImageAnalyze{Ask: fixedAsk("x")}})
	if _, ok := ro.Get("analyze_image"); !ok {
		t.Error("analyze_image withheld from a read-only session")
	}
}
