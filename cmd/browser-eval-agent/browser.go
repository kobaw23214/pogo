package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/tools"
)

var sessionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func validURL(raw string, worker bool) bool {
	u, err := url.Parse(raw)
	return len(raw) <= 8192 && err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && (!worker || (u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/")))
}

const maxReplyBytes = 12 * 1024 * 1024
const maxImageBytes = 6 * 1024 * 1024
const maxImageStoreBytes = 64 * 1024 * 1024

type browserLink struct {
	base, session, token string
	http                 *http.Client
	stop                 context.CancelCauseFunc
	mu                   sync.Mutex
	blocked              bool
	images               map[string]string
	imageBytes           int
	nextImage            int
}

func (*browserLink) BrowserBackend() string { return tools.BrowserBackendNative }

var _ tools.BrowserBackendProvider = (*browserLink)(nil)

func newBrowserLink(base, session, token string, stop context.CancelCauseFunc) *browserLink {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &browserLink{base: strings.TrimSuffix(base, "/"), session: session, token: token, stop: stop, images: map[string]string{}, http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("worker redirects are disabled") }}}
}
func (b *browserLink) request(ctx context.Context, method, path string, payload any, result any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return errors.New("invalid browser request")
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.base+"/sessions/"+url.PathEscape(b.session)+path, body)
	if err != nil {
		return errors.New("invalid browser request")
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Content-Type", "application/json")
	response, err := b.http.Do(req)
	if err != nil {
		return errors.New("browser transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("browser service returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxReplyBytes+1))
	if err != nil {
		return errors.New("browser response interrupted")
	}
	if len(data) > maxReplyBytes {
		return errors.New("browser response exceeded size limit")
	}
	if result != nil && json.Unmarshal(data, result) != nil {
		return errors.New("browser response was not valid JSON")
	}
	return nil
}
func (b *browserLink) create(ctx context.Context) error {
	return b.request(ctx, http.MethodPut, "", nil, nil)
}
func (b *browserLink) cancel() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var ack struct {
		OK bool `json:"ok"`
	}
	if err := b.request(ctx, http.MethodPost, "/cancel", map[string]any{}, &ack); err != nil {
		return err
	}
	if !ack.OK {
		return errors.New("browser cancellation not acknowledged")
	}
	return nil
}
func (b *browserLink) Execute(ctx context.Context, op json.RawMessage) (tools.BrowserReply, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.blocked {
		return tools.BrowserReply{}, errBrowser
	}
	var args map[string]any
	if json.Unmarshal(op, &args) != nil || args == nil {
		return tools.BrowserReply{}, errors.New("invalid browser operation")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return tools.BrowserReply{}, errors.New("operation ID unavailable")
	}
	args["op_id"] = hex.EncodeToString(nonce[:])
	var reply tools.BrowserReply
	if err := b.request(ctx, http.MethodPost, "/execute", args, &reply); err != nil {
		b.blocked = true
		// Fail closed: never automatically retry an operation, even after cancel.
		// The caller owns cleanup and waits for a separately bounded cancel ACK.
		b.stop(errBrowser)
		return tools.BrowserReply{}, errBrowser
	}
	if im := reply.Screenshot; im != nil {
		if err := b.storeImage(im); err != nil {
			b.blocked = true
			b.stop(errBrowser)
			return tools.BrowserReply{}, err
		}
	}
	return reply, nil
}
func (b *browserLink) storeImage(im *tools.BrowserScreenshot) error {
	if im.Data == "" || (im.Mime != "image/jpeg" && im.Mime != "image/png") || im.Width < 1 || im.Height < 1 || im.Width > 8192 || im.Height > 8192 {
		return errors.New("invalid browser screenshot")
	}
	data, err := base64.StdEncoding.DecodeString(im.Data)
	if err != nil || len(data) > maxImageBytes {
		return errors.New("invalid or oversized browser screenshot")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != im.Width || cfg.Height != im.Height || "image/"+format != im.Mime {
		return errors.New("browser screenshot metadata mismatch")
	}
	if b.imageBytes+len(data) > maxImageStoreBytes {
		return errors.New("browser screenshot memory limit reached")
	}
	b.nextImage++
	ref := fmt.Sprintf("eval-memory/browser-%d.%s", b.nextImage, format)
	b.images[ref] = "data:" + im.Mime + ";base64," + im.Data
	b.imageBytes += len(data)
	im.Data = ""
	im.ImageRef = ref
	return nil
}
func (b *browserLink) resolveImage(ref string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.images[ref]
	return data, ok
}
