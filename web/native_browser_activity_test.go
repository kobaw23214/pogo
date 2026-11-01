package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func nativeActivityFixture() map[string]any {
	return map[string]any{
		"seq": 1, "id": "a1", "actor": "agent", "action": "type", "phase": "succeeded",
		"label": "must-not-export-input", "tab_id": "t1", "version": "1:t1:1:2:abc", "started_at": 1000, "updated_at": 1001,
		"password": "must-not-export-password", "value": "must-not-export-value",
	}
}

func nativeOverlayFixture() map[string]any {
	return map[string]any{
		"version": "1:t1:1:2:abc", "tab_id": "t1", "sampled_at": 1002,
		"viewport": map[string]any{"width": 1280, "height": 800}, "last_action": nativeActivityFixture(),
		"pointer":    map[string]any{"x": 45, "y": 65, "seq": 1, "action_seq": 1, "kind": "click", "actor": "agent"},
		"target":     map[string]any{"rect": map[string]any{"x": 20, "y": 50, "width": 100, "height": 40}},
		"focus":      map[string]any{"rect": map[string]any{"x": 20, "y": 50, "width": 100, "height": 40}, "editable": true, "value": "must-not-export-focus"},
		"worker_url": "must-not-export-worker",
	}
}

func encodeNativeOverlay(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func TestNativeBrowserActivityStripsInputAndValidatesStages(t *testing.T) {
	activity := map[string]any{"last_action": nativeActivityFixture(), "recent_actions": []any{nativeActivityFixture()}, "value": "must-not-export"}
	raw, _ := json.Marshal(map[string]any{"activity": activity, "control": "agent"})
	state, err := nativeBrowserState(raw, false)
	if err != nil || len(state["activity"]) == 0 || bytes.Contains(state["activity"], []byte("must-not-export")) || !bytes.Contains(state["activity"], []byte("Filling field")) {
		t.Fatalf("unsafe or missing activity: %s %v", state["activity"], err)
	}
	for _, invalid := range []string{"complete", "cancelled", "<script>"} {
		bad := nativeActivityFixture()
		bad["phase"] = invalid
		raw, _ := json.Marshal(map[string]any{"last_action": bad})
		if sanitizeNativeActivity(raw) != nil {
			t.Fatalf("accepted invalid phase %q", invalid)
		}
	}
}

func TestNativeBrowserActivityKeepsBackAndCalculateInRecentHistory(t *testing.T) {
	for _, action := range []string{"input_back", "calculate", "upload"} {
		entry := nativeActivityFixture()
		entry["action"] = action
		raw, _ := json.Marshal(map[string]any{"last_action": nativeActivityFixture(), "recent_actions": []any{entry}})
		clean := sanitizeNativeActivity(raw)
		if len(clean) == 0 || bytes.Contains(clean, []byte("must-not-export")) {
			t.Fatalf("valid recent %s action hid safe activity: %s", action, clean)
		}
	}
}

func TestNativeBrowserDebugStatePreservesOnlyStatusAndKnownActivity(t *testing.T) {
	activity := nativeActivityFixture()
	activity["action"] = "devtools"
	raw, _ := json.Marshal(map[string]any{
		"debugger_paused": true, "devtools_blocked": false,
		"cdp_endpoint": "must-not-export", "sessionId": "must-not-export",
		"activity": map[string]any{"last_action": activity},
	})
	state, err := nativeBrowserState(raw, false)
	if err != nil || string(state["debugger_paused"]) != "true" || string(state["devtools_blocked"]) != "false" || len(state["activity"]) == 0 {
		t.Fatalf("missing debug state: %s %v", state, err)
	}
	encoded, _ := json.Marshal(state)
	if bytes.Contains(encoded, []byte("must-not-export")) {
		t.Fatalf("debug state disclosed internal fields: %s", encoded)
	}
	state, err = nativeBrowserState([]byte(`{"debugger_paused":"true","devtools_blocked":{"secret":"value"}}`), false)
	if err != nil || len(state["debugger_paused"]) != 0 || len(state["devtools_blocked"]) != 0 {
		t.Fatalf("malformed debug flags accepted: %s %v", state, err)
	}
}

func TestNativeBrowserLoadingStateOnlyForwardsPublicContract(t *testing.T) {
	for _, loading := range []bool{true, false} {
		for _, message := range []string{"Page navigation failed.", "Page navigation was cancelled."} {
			raw, _ := json.Marshal(map[string]any{
				"page_loading": loading, "page_load_error": message, "frame_identity": "12:t3:44",
				"navigation_url": "https://private.invalid/?token=must-not-export", "loader_id": "must-not-export",
				"loading_details": map[string]any{"error": "must-not-export"}, "sessionId": "must-not-export",
			})
			state, err := nativeBrowserState(raw, false)
			if err != nil || string(state["page_loading"]) != string(rawBool(loading)) || string(state["frame_identity"]) != `"12:t3:44"` {
				t.Fatalf("public loading state lost: %s %v", state, err)
			}
			var got string
			if json.Unmarshal(state["page_load_error"], &got) != nil || got != message {
				t.Fatal("fixed load error lost")
			}
			encoded, _ := json.Marshal(state)
			if bytes.Contains(encoded, []byte("must-not-export")) {
				t.Fatal("loading state disclosed internal navigation data")
			}
		}
	}
	for _, body := range []map[string]any{
		{"page_loading": "true", "page_load_error": map[string]string{"error": "private"}, "frame_identity": 123},
		{"page_loading": 1, "page_load_error": "https://private.invalid/?token=private", "frame_identity": "cdp-target-private"},
		{"page_loading": nil, "page_load_error": strings.Repeat("x", 65), "frame_identity": strings.Repeat("1", 65)},
		{"page_load_error": "Page navigation failed.\nprivate diagnostic", "frame_identity": "1:t1:0"},
	} {
		raw, _ := json.Marshal(body)
		state, err := nativeBrowserState(raw, false)
		if err != nil || len(state["page_loading"]) != 0 || len(state["page_load_error"]) != 0 || len(state["frame_identity"]) != 0 {
			t.Fatal("malformed or private loading metadata accepted")
		}
	}
}

func rawBool(value bool) json.RawMessage {
	if value {
		return json.RawMessage("true")
	}
	return json.RawMessage("false")
}

func TestNativeBrowserFrameRequiresPublicDocumentIdentity(t *testing.T) {
	identity := ""
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/frame") {
			w.Header().Set("X-Browser-Frame-Identity", identity)
			_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0x01})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	b := newNativeBrowser(cfg)
	t.Cleanup(b.Close)
	for _, invalid := range []string{"", "cdp-target-private", "1:t1:0", "1:t1:1:extra", strings.Repeat("1", 65)} {
		identity = invalid
		frame, err := b.Frame(context.Background())
		if err == nil || len(frame.Data) != 0 || frame.Identity != "" {
			t.Fatal("unidentified pixels were accepted")
		}
		if invalid != "" && strings.Contains(err.Error(), invalid) {
			t.Fatal("invalid frame identity leaked through a diagnostic")
		}
	}
	identity = "12:t3:44"
	frame, err := b.Frame(context.Background())
	if err != nil || frame.Identity != identity || len(frame.Data) == 0 || frame.Version != "" {
		t.Fatal("identified display-only frame was rejected")
	}
}

func TestNativeBrowserFrameOverlayIsBoundedAndMatchesPixels(t *testing.T) {
	version := "1:t1:1:2:abc"
	valid := encodeNativeOverlay(t, nativeOverlayFixture())
	header := valid
	cfg := nativeTestConfig(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/frame") {
			w.Header().Set("X-Browser-Frame-Identity", "1:t1:1")
			w.Header().Set("X-Browser-Version", version)
			w.Header().Set("X-Browser-Overlay", header)
			_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0x01})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	b := newNativeBrowser(cfg)
	t.Cleanup(b.Close)
	frame, err := b.Frame(context.Background())
	raw, _ := base64.RawURLEncoding.DecodeString(frame.Overlay)
	if err != nil || len(frame.Data) == 0 || frame.Version != version || frame.Identity != "1:t1:1" || len(raw) == 0 || bytes.Contains(raw, []byte("must-not-export")) {
		t.Fatalf("unsafe frame overlay: %s %v", raw, err)
	}
	for _, mutate := range []func(map[string]any){
		func(m map[string]any) { m["version"] = "old-frame" },
		func(m map[string]any) { m["tab_id"] = "t2" },
		func(m map[string]any) { m["viewport"] = map[string]any{"width": 0, "height": 800} },
		func(m map[string]any) { m["pointer"].(map[string]any)["x"] = 2000 },
		func(m map[string]any) { m["pointer"].(map[string]any)["seq"] = -1 },
		func(m map[string]any) { m["focus"].(map[string]any)["rect"].(map[string]any)["width"] = 2000 },
	} {
		value := nativeOverlayFixture()
		mutate(value)
		header = encodeNativeOverlay(t, value)
		frame, err = b.Frame(context.Background())
		if err != nil || len(frame.Data) == 0 || frame.Overlay != "" {
			t.Fatal("invalid overlay was forwarded or hid the valid JPEG")
		}
	}
	for _, invalid := range []string{"%not-base64", strings.Repeat("a", 13<<10), ""} {
		if sanitizeNativeOverlay(invalid, version) != "" {
			t.Fatal("invalid header accepted")
		}
	}
}
