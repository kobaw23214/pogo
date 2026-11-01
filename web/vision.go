// The vision helper: one model, configured as vision_model in providers.json,
// that sees images on behalf of text-only conversation models. It writes the
// bootstrap description an attachment carries, and answers analyze_image.
//
// Everything degrades: no vision_model configured, or the helper unreachable,
// never blocks a message — the attachment note then says so, and the model (or
// the user) works with what is there.
package web

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/tools"
)

// ErrNoVisionModel is the configured-out state: images still upload and render,
// they just never get described or queried.
var ErrNoVisionModel = errors.New("no vision_model configured in providers.json")

// visionDescribePrompt is written for a downstream reader that cannot see the
// picture: structure over prose, verbatim text over paraphrase, no advice. The
// verbatim rule exists because VLMs paraphrase dense text into plausible
// neighbours (the OCR-hallucination literature's one-line summary), and a
// coding agent acting on "error 401" the image never showed is worse off than
// one told the text was unreadable.
const visionDescribePrompt = "你在为一位看不到这张图的下游编程助手写描述。" +
	"用中文按此结构输出，不写建议、不猜测图外内容：\n" +
	"1. 类型：截图/照片/图表/界面/终端/代码/其他\n" +
	"2. 图中可见文字的逐字转录（多列内容用表格或竖线分隔；无法辨认的标为〔不清〕）\n" +
	"3. 主要元素与布局（位置关系、层级）\n" +
	"4. 关键颜色与风格（如有）\n" +
	"5. 与软件调试相关的细节（报错、弹窗、高亮、角标、状态灯）"

// visionAnswerPrefix keeps the ask path terse: the question already carries
// the context, the answer should be the answer.
const visionAnswerPrefix = "基于图片内容回答。引用图中文字时逐字引用，无法辨认就说明；不要给建议除非问题要求。问题："

// visionCallBudget caps one helper call. A local model may spend most of this
// on the first cold load (tens of seconds for a 12B), so the ceiling is a
// guard against a hang, not a tune.
const visionCallBudget = 120 * time.Second

// visionClient returns (and memoises) a client for the configured vision
// model. Settings reloads can change vision_model; the map is keyed by model
// name so a re-resolve after reload gets a fresh client.
func (m *Manager) visionClient() (llm.Client, error) {
	name := visionModelName()
	if name == "" {
		return nil, ErrNoVisionModel
	}
	m.visionMu.Lock()
	defer m.visionMu.Unlock()
	if c, ok := m.visionClients[name]; ok {
		return c, nil
	}
	cfg, err := visionResolve(name)
	if err != nil {
		return nil, err
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		// Local servers default low (ollama's num_predict is 128 without this),
		// which truncates a structured description mid-table.
		maxTokens = 2048
	}
	c := llm.New(llm.Options{
		BaseURL:       cfg.BaseURL,
		APIKey:        cfg.APIKey,
		Model:         cfg.Model,
		MaxTokens:     maxTokens,
		Headers:       cfg.Headers,
		ExtraBody:     cfg.ExtraBody,
		Compatibility: cfg.Compatibility,
		MaxRetries:    1,
	})
	if m.visionClients == nil {
		m.visionClients = map[string]llm.Client{}
	}
	m.visionClients[name] = c
	return c, nil
}

// visionModelName and visionResolve are seams for tests, on the schedDetect
// pattern: the production pair reads the live config, tests script answers.
var (
	visionModelName = func() string { return config.VisionModel() }
	visionResolve   = func(name string) (config.Resolved, error) { return config.Resolve(name) }
)

// attachmentDataURL loads a workspace image and frames it as the one wire form
// every provider in the catalog accepts.
func attachmentDataURL(cwd, rel string) (string, error) {
	abs, err := attachAbs(cwd, rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	mime := http.DetectContentType(data)
	if _, ok := attachImageExts[mime]; !ok {
		return "", fmt.Errorf("%s is not a displayable image", rel)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// visionAsk answers one question about one workspace image, for the
// analyze_image tool. Results are memoised in-process by cwd+ref+question:
// sessions on different workspaces with the same upload path are different
// files, and a model that re-asks a question it already asked should be free.
func (m *Manager) visionAsk(ctx context.Context, cwd, ref, question string) (string, error) {
	key := cwd + "\x00" + ref + "\x00" + question
	m.visionMu.Lock()
	if ans, ok := m.visionCache[key]; ok {
		m.visionMu.Unlock()
		return ans, nil
	}
	m.visionMu.Unlock()

	c, err := m.visionClient()
	if err != nil {
		return "", err
	}
	dataURL, err := attachmentDataURL(cwd, ref)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, visionCallBudget)
	defer cancel()
	answer, err := visionExchange(ctx, c, dataURL, visionAnswerPrefix+question)
	if err != nil {
		return "", err
	}
	m.visionMu.Lock()
	if m.visionCache == nil {
		m.visionCache = map[string]string{}
	}
	m.visionCache[key] = answer
	m.visionMu.Unlock()
	return answer, nil
}

// describeImage produces (and caches beside the file) the bootstrap
// description a text-only conversation model receives with an attachment.
// The cache key is the content hash, so the same picture re-uploaded anywhere
// in the session describes once.
func (m *Manager) describeImage(ctx context.Context, cwd, rel string) (string, bool) {
	hash, err := attachmentHash(cwd, rel)
	if err != nil {
		return "", false
	}
	cacheDir := filepath.Join(cwd, UploadsDir, attachCacheDir)
	if raw, err := os.ReadFile(filepath.Join(cacheDir, hash+".txt")); err == nil && len(raw) > 0 {
		return string(raw), true
	}
	c, err := m.visionClient()
	if err != nil {
		return "", false
	}
	dataURL, err := attachmentDataURL(cwd, rel)
	if err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, visionCallBudget)
	defer cancel()
	text, err := visionExchange(ctx, c, dataURL, visionDescribePrompt)
	if err != nil || strings.TrimSpace(text) == "" {
		return "", false
	}
	_ = os.MkdirAll(cacheDir, 0o755)
	_ = os.WriteFile(filepath.Join(cacheDir, hash+".txt"), []byte(text), 0o644)
	return text, true
}

// visionExchange is one non-streaming round trip with the helper: image first,
// instruction after — every provider's documented preference.
var visionExchange = func(ctx context.Context, c llm.Client, dataURL, prompt string) (string, error) {
	history := []llm.Message{{
		Role: llm.RoleUser,
		Content: []llm.Block{
			{Type: llm.BlockImage, ImageRef: "ask", DataURL: dataURL},
			{Type: llm.BlockText, Text: prompt},
		},
	}}
	resp, err := c.Stream(ctx, "", history, nil, nil)
	if err != nil {
		return "", fmt.Errorf("vision model call failed: %w", err)
	}
	return resp.Message.Text(), nil
}

// --- session-side wiring ---

// imageResolver is the agent's Config.ImageResolver: pixels for a model that
// sees them, nothing for one that does not (convert.go substitutes the
// description). Reads the live cfg because SetModel can swap mid-session.
func (s *Session) imageResolver(ref string) (string, bool) {
	s.mu.Lock()
	sees := s.cfg.SeesImages
	cwd := s.cwd
	s.mu.Unlock()
	if !sees {
		return "", false
	}
	url, err := attachmentDataURL(cwd, ref)
	if err != nil {
		return "", false
	}
	return url, true
}

// recentImages snapshots the attachment list for the tool's name resolution.
func (s *Session) recentImages() []tools.ImageHandle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]tools.ImageHandle(nil), s.images...)
}

// seedImages rebuilds the attachment list from a loaded transcript, capped at
// the most recent fifty: name resolution cares about what the model can
// plausibly still be asking about, not an exhaustive history.
func (s *Session) seedImages(history []session.Timed) {
	for _, t := range history {
		for _, b := range t.Message.Content {
			for _, im := range b.Images {
				if im.Type == llm.BlockImage {
					s.images = append(s.images, tools.ImageHandle{Ref: im.ImageRef, Name: im.ImageName})
				}
			}
			if b.Type == llm.BlockImage {
				s.images = append(s.images, tools.ImageHandle{Ref: b.ImageRef, Name: b.ImageName})
			}
		}
	}
	if len(s.images) > 50 {
		s.images = append([]tools.ImageHandle(nil), s.images[len(s.images)-50:]...)
	}
}

// imageTool builds the analyze_image registration for a session, or nil when
// no vision_model is configured — an unanswerable tool would cost schema
// tokens and a wasted call to discover that.
func (m *Manager) imageTool(s *Session) *tools.ImageAnalyze {
	if visionModelName() == "" {
		return nil
	}
	return &tools.ImageAnalyze{Ask: s.visionAsk, Recent: s.recentImages}
}

func browserModelImageMode(cfg config.Resolved) tools.BrowserImageMode {
	if cfg.SeesImages {
		return tools.BrowserImageDirect
	}
	return tools.BrowserImageNone
}

func (s *Session) visionAsk(ctx context.Context, ref, question string) (string, error) {
	s.mu.Lock()
	cwd := s.cwd
	s.mu.Unlock()
	return s.mgr.visionAsk(ctx, cwd, ref, question)
}
