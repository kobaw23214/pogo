package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"
)

type nativeBrowserPickInput struct {
	ContextRef string   `json:"context_ref"`
	X          *float64 `json:"x"`
	Y          *float64 `json:"y"`
	Version    string   `json:"version"`
	TabID      string   `json:"tab_id"`
	// Preserve presence so null, non-string and malformed tokens cannot be
	// silently downgraded into the legacy strict selection path.
	PreviewToken json.RawMessage `json:"preview_token,omitempty"`
}

type nativeBrowserPickResult struct {
	ContextRef string `json:"context_ref"`
	Version    string `json:"version"`
	TabID      string `json:"tab_id"`
	URL        string `json:"url"`
	Title      string `json:"title"`
	Viewport   struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	} `json:"viewport"`
	Element *struct {
		Ref      string            `json:"ref"`
		Tag      string            `json:"tag"`
		Role     string            `json:"role"`
		Name     string            `json:"name"`
		Text     string            `json:"text"`
		Rect     nativeBrowserRect `json:"rect"`
		FrameURL string            `json:"frame_url,omitempty"`
		Fallback string            `json:"fallback,omitempty"`
	} `json:"element"`
}

var nativePickRef = regexp.MustCompile(`^p[1-9][0-9]{0,15}$`)
var nativePickPreviewToken = regexp.MustCompile(`^pv_[a-f0-9]{48}$`)

func validNativePickPreviewToken(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var token string
	return json.Unmarshal(raw, &token) == nil && nativePickPreviewToken.MatchString(token)
}

func validNativePickInput(input nativeBrowserPickInput) bool {
	return input.X != nil && input.Y != nil && finiteNativeNumber(*input.X) && finiteNativeNumber(*input.Y) &&
		*input.X >= 0 && *input.Y >= 0 && *input.X < 8192 && *input.Y < 8192 &&
		validNativeContextRef(input.ContextRef) && input.Version != "" && len(input.Version) <= 256 && nativeTabID.MatchString(input.TabID) && validNativePickPreviewToken(input.PreviewToken)
}

// Decode an explicit public shape: page HTML, form values, service credentials
// and arbitrary worker metadata never become composer context by accident.
func decodeNativeBrowserPick(raw []byte, input nativeBrowserPickInput) (*nativeBrowserPickResult, error) {
	invalid := errors.New("invalid or stale native browser element observation")
	if len(raw) > 32<<10 || !validNativePickInput(input) {
		return nil, invalid
	}
	var result nativeBrowserPickResult
	if json.Unmarshal(raw, &result) != nil || result.Version != input.Version || result.TabID != input.TabID || result.ContextRef != input.ContextRef {
		return nil, invalid
	}
	w, h := result.Viewport.Width, result.Viewport.Height
	if !finiteNativeNumber(w) || !finiteNativeNumber(h) || w < 1 || h < 1 || w > 8192 || h > 8192 || *input.X >= w || *input.Y >= h ||
		utf8.RuneCountInString(result.URL) > 4096 || utf8.RuneCountInString(result.Title) > 300 {
		return nil, invalid
	}
	if el := result.Element; el != nil {
		if !nativePickRef.MatchString(el.Ref) || el.Tag == "" || utf8.RuneCountInString(el.Tag) > 32 ||
			utf8.RuneCountInString(el.Role) > 80 || utf8.RuneCountInString(el.Name) > 300 || utf8.RuneCountInString(el.Text) > 800 ||
			utf8.RuneCountInString(el.FrameURL) > 4096 || !validNativeRect(el.Rect, w, h) ||
			*input.X < el.Rect.X-0.01 || *input.Y < el.Rect.Y-0.01 || *input.X > el.Rect.X+el.Rect.Width+0.01 || *input.Y > el.Rect.Y+el.Rect.Height+0.01 ||
			(el.Fallback != "" && el.Fallback != "frame" && el.Fallback != "shadow") {
			return nil, invalid
		}
	}
	return &result, nil
}

func (b *nativeBrowser) Pick(ctx context.Context, input nativeBrowserPickInput) (*nativeBrowserPickResult, error) {
	if !validNativePickInput(input) {
		return nil, &nativeBrowserHTTPError{status: http.StatusBadRequest, message: "element selection requires coordinates, tab and frame version"}
	}
	// The worker fences observations against the selected page's action and
	// document identity. A background Sheet can remain selectable while the
	// Agent operates a different page; do not seize its execution lock here.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	blocked := b.blocked != "" || b.cleanupRequired
	b.mu.Unlock()
	if blocked {
		return nil, &nativeBrowserHTTPError{status: http.StatusConflict, message: "browser is recovering; refresh before selecting an element"}
	}
	raw, err := b.request(ctx, http.MethodPost, "/pick", input)
	if err != nil {
		// A read-only timeout is safe to discard. Cancelling the browser here
		// could close an unrelated active page or interrupt a human takeover.
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return decodeNativeBrowserPick(raw, input)
}

func (s *Server) handleBrowserPick(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.nativeBrowserSession(w, r)
	if !ok {
		return
	}
	var input nativeBrowserPickInput
	if err := decodeNativeBrowserBody(r, &input); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !validNativePickInput(input) {
		writeErr(w, http.StatusBadRequest, errors.New("element selection requires coordinates, tab and frame version"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	result, err := sess.nativeBrowser.Pick(ctx, input)
	if err != nil {
		writeNativeBrowserError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	sess.touch()
	writeJSON(w, http.StatusOK, result)
}
