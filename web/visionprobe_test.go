package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGradeVisionReply(t *testing.T) {
	q := visionQuestions[0] // colour: C 紫色
	for _, reply := range []string{"C", "c", "C。", "答案是C", "The answer is C."} {
		if !gradeVisionReply(reply, q) {
			t.Fatalf("letter reply %q should pass", reply)
		}
	}
	for _, reply := range []string{"A", "B", "D", "答案是A", ""} {
		if gradeVisionReply(reply, q) {
			t.Fatalf("wrong reply %q should fail", reply)
		}
	}
	// A model that ignored the letter instruction but named the colour still
	// saw the picture.
	if !gradeVisionReply("紫色", q) || gradeVisionReply("红色", q) {
		t.Fatal("option-text fallback misgraded")
	}
	// Letters inside words must not read as options ("Image" has an A).
	if gradeVisionReply("Image not available", q) {
		t.Fatal("letter inside a word matched")
	}
	// The digit question grades its own option texts.
	digit := visionQuestions[1]
	if !gradeVisionReply("图中数字是 7", digit) || gradeVisionReply("图中数字是 9", digit) {
		t.Fatal("digit text fallback misgraded")
	}
}

// runVisionProbe points the probe at a gateway and decodes the result.
func runVisionProbe(t *testing.T, gateway *httptest.Server, model string) visionProbeResult {
	t.Helper()
	h := newHarness(t, idleRespond)
	payload, _ := json.Marshal(map[string]any{
		"base_url": gateway.URL,
		"key":      "sk-typed", // typed keys may reach hosts the config does not name
		"model":    model,
	})
	var got visionProbeResult
	if err := decodeJSONBody(h.do(http.MethodPost, "/api/config/vision-probe", string(payload)), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// chatGateway is an OpenAI-shaped upstream whose reply is decided by a
// callback on the incoming question text; it also records the last request's
// message shape so the wire form can be asserted.
func chatGateway(t *testing.T, replyFor func(ask string) (int, string)) (*httptest.Server, *map[string]any) {
	t.Helper()
	var lastReq map[string]any
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		lastReq = parsed
		ask := ""
		if msgs, ok := parsed["messages"].([]any); ok && len(msgs) > 0 {
			if m, ok := msgs[0].(map[string]any); ok {
				if parts, ok := m["content"].([]any); ok && len(parts) > 0 {
					if p, ok := parts[0].(map[string]any); ok {
						ask, _ = p["text"].(string)
					}
				}
			}
		}
		status, reply := replyFor(ask)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": reply},
			}},
		})
	}))
	t.Cleanup(gw.Close)
	return gw, &lastReq
}

func TestVisionProbeQuestionsServesTheRealMaterial(t *testing.T) {
	h := newHarness(t, idleRespond)
	var got struct {
		Questions []struct {
			Ask      string `json:"ask"`
			ImageURL string `json:"image_url"`
		} `json:"questions"`
	}
	if err := decodeJSONBody(h.do(http.MethodGet, "/api/config/vision-probe/questions", ""), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Questions) != 3 {
		t.Fatalf("want 3 questions, got %d", len(got.Questions))
	}
	for _, q := range got.Questions {
		if q.Ask == "" || !strings.HasPrefix(q.ImageURL, "data:image/png;base64,") || len(q.ImageURL) < 200 {
			t.Fatalf("material not served verbatim: %+v", q)
		}
	}
}

func TestVisionProbeThreeRightAnswersSayYes(t *testing.T) {
	gw, lastReq := chatGateway(t, func(ask string) (int, string) {
		switch {
		case strings.Contains(ask, "色块"):
			return 200, "C"
		case strings.Contains(ask, "数字"):
			return 200, "B"
		default:
			return 200, "A"
		}
	})
	res := runVisionProbe(t, gw, "vision-model")
	if !res.OK || res.Verdict != "yes" || len(res.Questions) != 3 {
		t.Fatalf("verdict: %+v", res)
	}
	for _, q := range res.Questions {
		if !q.Pass {
			t.Fatalf("question should pass: %+v", q)
		}
	}
	// The wire form is the agent's real one: a multimodal content array with
	// an inline data-URL image, not a URL the gateway would have to fetch.
	req := *lastReq
	msgs := req["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].([]any)
	img := content[1].(map[string]any)
	if img["type"] != "image_url" {
		t.Fatalf("second content part is not an image: %v", img)
	}
	url := img["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") || len(url) < 100 {
		t.Fatalf("image url is not an inline data URL: %q", url[:min(40, len(url))])
	}
}

func TestVisionProbeTextFallbackAndWrongAnswersSayNo(t *testing.T) {
	// Right answers spelled as the option text, not the letter: still a pass.
	gw, _ := chatGateway(t, func(ask string) (int, string) {
		switch {
		case strings.Contains(ask, "色块"):
			return 200, "紫色"
		case strings.Contains(ask, "数字"):
			return 200, "图中数字是 7"
		default:
			return 200, "3 个"
		}
	})
	if res := runVisionProbe(t, gw, "m"); res.Verdict != "yes" {
		t.Fatalf("text replies should still pass: %+v", res)
	}

	// A model that cannot see the picture guesses or says so.
	gw2, _ := chatGateway(t, func(ask string) (int, string) {
		return 200, "抱歉，我看不到图片。"
	})
	res := runVisionProbe(t, gw2, "m")
	if !res.OK || res.Verdict != "no" || res.Rejected {
		t.Fatalf("blind replies should be verdict no: %+v", res)
	}
	for _, q := range res.Questions {
		if q.Pass {
			t.Fatalf("a blind reply should not pass: %+v", q)
		}
	}
}

func TestVisionProbeEndpointRefusalIsRejectedNo(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"this model does not support image input"}}`))
	}))
	defer gw.Close()
	res := runVisionProbe(t, gw, "m")
	if res.Verdict != "no" || !res.Rejected || !strings.Contains(res.Note, "image") {
		t.Fatalf("image refusal should be rejected-no: %+v", res)
	}
	// An unrelated failure says nothing about vision: the question stays
	// open rather than being misread as "no".
	gw2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer gw2.Close()
	res2 := runVisionProbe(t, gw2, "m")
	if res2.OK || res2.Verdict != "" || res2.Error == "" {
		t.Fatalf("unrelated error should be inconclusive: %+v", res2)
	}
}
