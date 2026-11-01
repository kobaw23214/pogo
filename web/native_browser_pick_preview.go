package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
	"unicode/utf8"
)

// Preview is geometry-only hover feedback. It never becomes an element
// citation, action reference or evidence source without a separate strict pick.
type nativeBrowserPickPreviewResult struct {
	ContextRef string `json:"context_ref"`
	Version    string `json:"version"`
	TabID      string `json:"tab_id"`
	Viewport   struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	} `json:"viewport"`
	Element *struct {
		Tag      string            `json:"tag"`
		Role     string            `json:"role"`
		Rect     nativeBrowserRect `json:"rect"`
		Fallback string            `json:"fallback,omitempty"`
	} `json:"element"`
	PreviewToken string `json:"preview_token,omitempty"`
	ExpiresInMS  *int64 `json:"expires_in_ms,omitempty"`
}

func validNativePickPreviewInput(input nativeBrowserPickInput) bool {
	return validNativePickInput(input) && len(input.PreviewToken) == 0
}

func decodeNativeBrowserPickPreview(raw []byte, input nativeBrowserPickInput) (*nativeBrowserPickPreviewResult, error) {
	invalid := errors.New("invalid or stale browser element preview")
	if len(raw) > 8<<10 || !validNativePickPreviewInput(input) {
		return nil, invalid
	}
	var result nativeBrowserPickPreviewResult
	if json.Unmarshal(raw, &result) != nil || result.ContextRef != input.ContextRef || result.TabID != input.TabID || result.Version != input.Version {
		return nil, invalid
	}
	w, h := result.Viewport.Width, result.Viewport.Height
	if !finiteNativeNumber(w) || !finiteNativeNumber(h) || w < 1 || h < 1 || w > 8192 || h > 8192 || *input.X >= w || *input.Y >= h {
		return nil, invalid
	}
	if el := result.Element; el != nil {
		if el.Tag == "" || utf8.RuneCountInString(el.Tag) > 32 || utf8.RuneCountInString(el.Role) > 80 || !validNativeRect(el.Rect, w, h) ||
			*input.X < el.Rect.X-0.01 || *input.Y < el.Rect.Y-0.01 || *input.X > el.Rect.X+el.Rect.Width+0.01 || *input.Y > el.Rect.Y+el.Rect.Height+0.01 ||
			(el.Fallback != "" && el.Fallback != "frame") || !nativePickPreviewToken.MatchString(result.PreviewToken) || result.ExpiresInMS == nil || *result.ExpiresInMS != 3000 {
			return nil, invalid
		}
	} else if result.PreviewToken != "" || result.ExpiresInMS != nil {
		return nil, invalid
	}
	return &result, nil
}

func (b *nativeBrowser) PickPreview(ctx context.Context, input nativeBrowserPickInput) (*nativeBrowserPickPreviewResult, error) {
	if !validNativePickPreviewInput(input) {
		return nil, &nativeBrowserHTTPError{status: http.StatusBadRequest, message: "element preview requires only coordinates, context, tab and frame version"}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	blocked := b.blocked != "" || b.cleanupRequired || b.closed
	b.mu.Unlock()
	if blocked {
		return nil, &nativeBrowserHTTPError{status: http.StatusConflict, message: "browser is recovering; refresh before previewing an element"}
	}
	// Hover does not acquire the Agent queue or create a context. Losing a
	// read-only response must never cancel a different tab's ongoing action.
	raw, err := b.request(ctx, http.MethodPost, "/pick/preview", input)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return decodeNativeBrowserPickPreview(raw, input)
}

func (s *Server) handleBrowserPickPreview(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.nativeBrowserSession(w, r)
	if !ok {
		return
	}
	var input nativeBrowserPickInput
	if err := decodeNativeBrowserBody(r, &input); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !validNativePickPreviewInput(input) {
		writeErr(w, http.StatusBadRequest, errors.New("element preview requires only coordinates, context, tab and frame version"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	result, err := sess.nativeBrowser.PickPreview(ctx, input)
	if err != nil {
		writeNativeBrowserError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	sess.touch()
	writeJSON(w, http.StatusOK, result)
}
