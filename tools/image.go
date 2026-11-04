package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// ImageHandle names one image the conversation has attached, for resolving
// what a model meant by "the screenshot" or "the latest one".
type ImageHandle struct {
	Ref  string
	Name string
}

type ImageAnalyzeDetails struct {
	Path string `json:"path"`
}

// ImageAnalyze is the bridge that lets a text-only model ask a vision model
// about an image: the description attached alongside an image is a bootstrap,
// not a summary of everything anyone might ever ask about it, so the follow-up
// questions go through this tool rather than a bigger upfront description.
//
// Like Browser, the executor is not in this package: Ask carries the call to
// whatever vision helper the deployment configured, which keeps the tool free
// of provider knowledge and lets tests script the answer.
type ImageAnalyze struct {
	// Ask runs one question about one image, returning the vision model's text
	// answer. Required; nil means the tool is not constructed at all.
	Ask func(ctx context.Context, ref, question string) (string, error)
	// Recent lists this session's attached images, oldest first. The last entry
	// is what an image argument of "" or "latest" resolves to. Optional: without
	// it the image argument must be an explicit ref or name.
	Recent func() []ImageHandle
}

func (*ImageAnalyze) Name() string { return "analyze_image" }

func (t *ImageAnalyze) Description() string {
	return "Ask the vision model a question about an attached image or any image in the session workspace. " +
		"Use it whenever the [图片已附加 …] note in the conversation does not answer " +
		"what you need: exact text in the image, colors, layout, what changed " +
		"between two screenshots. The question may be specific (\"左上角按钮写的什么\") " +
		"and you may call this tool repeatedly about the same image.\n\n" +
		"image is the attachment's name or reference, or a workspace-relative/absolute image path; " +
		"omit it to ask about the most recently attached image."
}

func (t *ImageAnalyze) InputSchema() map[string]any {
	return objectOrdered([]string{"question"}, orderedProps{
		{"image", prop("string", "The image to ask about: an attachment name/reference or an image path inside the session workspace. Omit for the most recently attached image.")},
		{"question", prop("string", "What to look at or read in the image. Be specific; ask separately about unrelated things.")},
	})
}

// ExecutionMode reports analyze_image as Parallel: it changes nothing, so
// several questions about several images may overlap.
func (*ImageAnalyze) ExecutionMode() ExecutionMode { return Parallel }

func (t *ImageAnalyze) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	var in struct {
		Image    string `json:"image"`
		Question string `json:"question"`
	}
	if err := unmarshal(args, &in); err != nil {
		return Result{}, fmt.Errorf("analyze_image: %w", err)
	}
	question := strings.TrimSpace(in.Question)
	if question == "" {
		return Result{}, fmt.Errorf("analyze_image: question must not be empty")
	}

	ref, err := t.resolveRef(in.Image)
	if err != nil {
		return Result{}, fmt.Errorf("analyze_image: %w", err)
	}
	answer, err := t.Ask(ctx, ref, question)
	if err != nil {
		// An error is reported as text rather than a hard failure: the model
		// may retry with a different image or rephrase, and the conversation
		// should not die because a local model server was loading.
		return Result{Text: "vision model unavailable: " + err.Error(), Details: ImageAnalyzeDetails{Path: ref}}, nil
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return Result{Text: "(the vision model returned an empty answer; try rephrasing the question)", Details: ImageAnalyzeDetails{Path: ref}}, nil
	}
	return Result{Text: answer, Details: ImageAnalyzeDetails{Path: ref}}, nil
}

// resolveRef maps attachment names first, then falls back to an image path in
// the session workspace. Empty and "latest" retain the newest-attachment shorthand.
func (t *ImageAnalyze) resolveRef(image string) (string, error) {
	image = strings.TrimSpace(image)
	var attached []ImageHandle
	if t.Recent != nil {
		attached = t.Recent()
	}
	if image == "" || strings.EqualFold(image, "latest") {
		if len(attached) == 0 {
			return "", fmt.Errorf("this session has no attached images; provide an image path in the workspace")
		}
		last := attached[len(attached)-1]
		return last.Ref, nil
	}
	for _, h := range attached {
		if strings.EqualFold(h.Ref, image) || strings.EqualFold(h.Name, image) {
			return h.Ref, nil
		}
	}
	for _, h := range attached {
		if h.Name != "" && strings.HasSuffix(strings.ToLower(h.Name), strings.ToLower(image)) {
			return h.Ref, nil
		}
	}
	if looksLikeImagePath(image) {
		return image, nil
	}
	if len(attached) == 0 {
		return "", fmt.Errorf("this session has no attached images; provide an image path in the workspace")
	}
	return "", fmt.Errorf("no attached image matches %q (attached: %s); you can also provide an image path in the workspace", image, t.describeAttached(attached))
}

func looksLikeImagePath(image string) bool {
	if filepath.IsAbs(image) || strings.ContainsAny(image, `/\\`) {
		return true
	}
	switch strings.ToLower(filepath.Ext(image)) {
	case ".png", ".jpg", ".jpeg", ".webp":
		return true
	}
	return false
}

func (*ImageAnalyze) describeAttached(attached []ImageHandle) string {
	parts := make([]string, 0, len(attached))
	for _, h := range attached {
		if h.Name != "" {
			parts = append(parts, h.Name)
			continue
		}
		parts = append(parts, h.Ref)
	}
	return strings.Join(parts, ", ")
}
