package webservice

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathPreviewURLReceiptAndRedaction(t *testing.T) {
	token := strings.Repeat("a1", 24)
	base := "http://192.168.1.20:19770" + PathPreviewPrefix + token + "/"
	for _, raw := range []string{base, strings.Replace(base, "192.168.1.20", "localhost", 1), strings.Replace(base, "192.168.1.20", "[fd00::1]", 1)} {
		u, err := url.Parse(raw)
		if err != nil || !IsPathPreviewURL(u) {
			t.Fatalf("rejected canonical receipt %q: %v", raw, err)
		}
	}
	for _, raw := range []string{strings.TrimSuffix(base, "/"), base + "api", base + "?x=1", base + "?", base + "#secret", strings.Replace(base, "a1", "%611", 1), strings.Replace(base, "http://", "javascript://", 1), strings.Replace(base, "://", "://user:password@", 1), "http://192.168.1.20:19770/", "http://192.168.1.20:19770/api/", "http://localhost:19770/_pogo/web/short/"} {
		u, err := url.Parse(raw)
		if err == nil && validManagedPreviewURL(u) {
			t.Fatalf("accepted malformed preview receipt %q", raw)
		}
	}
	owner := "http://192.168.1.20:19770/a/agent?c=session&web_service=ws_123"
	var off *Manager
	for _, raw := range []string{base, base + "api?secret=1#section", PathPreviewPrefix + token + "/assets/app.js", "https://example.test/_pogo/web/" + token + "/", PathPreviewPrefix + token[:12] + "…", "http://127.0.0.1:19770" + PathPreviewPrefix + token[:15] + "…"} {
		clean := off.RedactPreviewURLs("site " + raw + " owner " + owner)
		if strings.Contains(clean, token) || !strings.Contains(clean, owner) || !strings.Contains(clean, "web-service:preview-redacted") {
			t.Fatalf("capability leaked or owner link changed: %q", clean)
		}
	}
	if clean := off.RedactPreviewURLs(owner); clean != owner {
		t.Fatalf("ordinary owner URL changed: %q", clean)
	}
	nested := `{"preview_url":"` + strings.ReplaceAll(base, "/", `\/`) + `","id":9007199254740993}`
	clean := off.RedactPreviewURLs(nested)
	if strings.Contains(clean, token) || !strings.Contains(clean, `"id":9007199254740993`) {
		t.Fatalf("escaped JSON text leaked a bearer or changed its integer: %q", clean)
	}
}

type pathReceiptGateway struct {
	*memoryGateway
	address string
	closed  int
}

func (g *pathReceiptGateway) Open(ctx context.Context, rec Record) (Grant, error) {
	grant, err := g.memoryGateway.Open(ctx, rec)
	grant.PreviewURL = g.address
	return grant, err
}

func (g *pathReceiptGateway) CloseGrant(context.Context, Record, string) error {
	g.closed++
	return nil
}

func TestManagerPathGrantSurvivesRestartWithoutPersistingToken(t *testing.T) {
	old, scope, _, original := managerFixture(t)
	_ = old.Close()
	token := strings.Repeat("a1", 24)
	gateway := &pathReceiptGateway{memoryGateway: original, address: "http://127.0.0.1:19770" + PathPreviewPrefix + token + "/"}
	opts := old.options
	opts.Gateway = gateway
	m, err := NewManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	raw, err := m.Execute(context.Background(), scope, startRequest("path-start", "site"))
	if err != nil {
		t.Fatal(err)
	}
	id := decodeRecord(t, raw).ServiceID
	waitManager(t, m, scope, id, "ready")
	raw, err = m.Execute(context.Background(), scope, Request{Action: "open", ServiceID: id, OperationID: "path-open"})
	if err != nil {
		t.Fatal(err)
	}
	var opened Grant
	if json.Unmarshal(raw, &opened) != nil || opened.PreviewURL != gateway.address {
		t.Fatalf("invalid path grant: %s", raw)
	}
	data, err := os.ReadFile(filepath.Join(opts.Root, "registry.json"))
	var state diskState
	if err != nil || strings.Contains(string(data), token) || json.Unmarshal(data, &state) != nil || len(state.PreviewDomains) != 0 {
		t.Fatalf("path bearer/IP DNS suffix persisted: %v", err)
	}
	gateway.address = "http://127.0.0.1:19770/api/"
	_, err = m.Execute(context.Background(), scope, Request{Action: "open", ServiceID: id, OperationID: "bad-path-open"})
	requireCode(t, err, "GATEWAY_UNAVAILABLE")
	if gateway.closed != 1 {
		t.Fatal("malformed path receipt did not revoke its grant")
	}
	_ = m.Close()
	restored, err := NewManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if clean := restored.RedactPreviewURLs(opened.PreviewURL); clean != "web-service:preview-redacted" {
		t.Fatalf("restart leaked old receipt: %q", clean)
	}
}
