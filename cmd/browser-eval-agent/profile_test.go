package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
)

func TestGeneralWebProfileLoadsRealSharedSkillOutsideRepository(t *testing.T) {
	content, err := os.ReadFile("../../skills/builtin/browser-use/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	fields, body := skills.ParseFrontmatter(string(content))
	if fields["name"] != "browser-use" || fields["browser-task"] != "true" {
		t.Fatal("shared skill lost its browser task contract")
	}
	// The isolated CLI must not discover a project skill or depend on the
	// source tree at runtime. A shadow with the same name cannot alter its prompt.
	dir := t.TempDir()
	shadow := filepath.Join(dir, ".pogo", "skills", "browser-use")
	if err := os.MkdirAll(shadow, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shadow, "SKILL.md"), []byte("UNTRUSTED_PROJECT_SKILL"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	selected, err := selectProfile("general-web")
	if err != nil || selected.id != "pogo-agent-general-web-v1" || selected.auth {
		t.Fatal(selected.id, selected.auth, err)
	}
	if selected.prompt != generalWebIsolationPrompt+"\n\n"+body {
		t.Fatal("general profile does not use the actual shared skill body")
	}
	if strings.Contains(selected.prompt, "UNTRUSTED_PROJECT_SKILL") || strings.Contains(selected.prompt, "\nname: browser-use\n") {
		t.Fatal("general profile included a project override or skill frontmatter")
	}
	bodyHash := sha256.Sum256([]byte(body))
	if selected.sharedSkillBodySHA256 != hex.EncodeToString(bodyHash[:]) {
		t.Fatal("shared skill hash does not describe the actual body")
	}
	end := endForProfile(config.Resolved{Model: "fixture-model"}, selected)
	promptHash := sha256.Sum256([]byte(selected.prompt))
	if end.SharedSkillBodySHA256 != selected.sharedSkillBodySHA256 || end.SystemPromptSHA256 != hex.EncodeToString(promptHash[:]) {
		t.Fatal("general profile metadata does not describe its actual prompt")
	}
	t.Logf("general_web_system_prompt_sha256=%s shared_skill_body_sha256=%s", end.SystemPromptSHA256, end.SharedSkillBodySHA256)
}

func TestGeneralWebProfileUsesRealAgentAndNaturalAnswerWithoutAuthOutcome(t *testing.T) {
	selected, err := selectProfile("general-web")
	if err != nil {
		t.Fatal(err)
	}
	worker := newWorkerFixture(t)
	t.Setenv("POGO_BROWSER_EVAL_TOKEN", "worker-secret")
	answer := "已看到当前页面的 Continue 按钮，页面地址为 https://fixture.invalid/start。"
	client := &fakeClient{t: t, wantSystem: selected.prompt, run: func(_ context.Context, n int, history []llm.Message) (llm.Response, error) {
		if n == 1 {
			return toolResponse("snapshot", llm.Usage{Input: 8, Output: 2}), nil
		}
		observed := false
		for _, message := range history {
			for _, block := range message.Content {
				if block.Type == llm.BlockToolResult && strings.Contains(block.Text, "[e1] button Continue") {
					observed = true
				}
			}
		}
		if !observed {
			t.Error("model did not receive the actual browser observation")
		}
		return answerResponse(answer, llm.Usage{Input: 8, Output: 2}), nil
	}}
	raw, _ := json.Marshal(defaultInput(worker.server.URL))
	var out, diag bytes.Buffer
	code := runCLI(context.Background(), []string{"--profile", "general-web"}, bytes.NewReader(raw), &out, &diag, func(string, string) (llm.Client, config.Resolved, error) {
		return client, config.Resolved{Model: "fixture-model"}, nil
	})
	if code != 0 || diag.Len() != 0 {
		t.Fatal(code, out.String(), diag.String())
	}
	end := assertEnd(t, decodeEvents(t, out.String()), "completed")
	if end["profile"] != selected.id || end["answer"] != answer || end["shared_skill_body_sha256"] != selected.sharedSkillBodySHA256 {
		t.Fatal("actual general profile result or source metadata missing", end)
	}
	if end["outcome"] != nil || end["outcome_error"] != nil {
		t.Fatal("general natural-language answer acquired auth-only parsing", end)
	}
	creates, actions, cancels := worker.counts()
	if creates != 1 || actions != 1 || cancels != 0 || client.calls != 2 {
		t.Fatal("general profile did not use the existing Agent browser path")
	}
}

func TestHistoricalProfilesRemainFrozenAndOmitSharedSkillMetadata(t *testing.T) {
	for _, historical := range []struct {
		name, id, hash string
		auth           bool
	}{
		{"browser", "pogo-agent-browser-only", "9abcefdd1e931ba0b19286c77b032587d5a02f8122bdbc5bfe7ecbd436a3ca95", false},
		{"auth", "pogo-agent-browser-auth", "f40aa9f8fe8569387a890410536ce86287258d27d7fe0550e3b61cba10229a85", true},
		{"auth-auto", "pogo-agent-browser-auth-auto-v2", "c1b9be86f177848141aff8d5adf40fa68d26a7c9c052d843a4059969eb8682cc", true},
	} {
		t.Run(historical.name, func(t *testing.T) {
			selected, err := selectProfile(historical.name)
			if err != nil || selected.id != historical.id || selected.auth != historical.auth || selected.sharedSkillBodySHA256 != "" {
				t.Fatal("historical profile selection changed", err)
			}
			end := endForProfile(config.Resolved{Model: "fixture-model"}, selected)
			if end.SystemPromptSHA256 != historical.hash {
				t.Fatal("historical prompt hash changed", end.SystemPromptSHA256)
			}
			raw, err := json.Marshal(end)
			if err != nil || strings.Contains(string(raw), "shared_skill") {
				t.Fatal("historical end record acquired shared skill metadata", err)
			}
		})
	}
}

func TestRetiredAuthProfilesRejectBeforeInputModelAndWorker(t *testing.T) {
	for _, name := range []string{"auth", "auth-auto"} {
		t.Run(name, func(t *testing.T) {
			worker := newWorkerFixture(t)
			request, _ := json.Marshal(defaultInput(worker.server.URL))
			for _, input := range []string{"not JSON", string(request)} {
				called := false
				var out, diag bytes.Buffer
				code := runCLI(context.Background(), []string{"--profile", name}, strings.NewReader(input), &out, &diag, func(string, string) (llm.Client, config.Resolved, error) {
					called = true
					return nil, config.Resolved{}, nil
				})
				if code != 2 || called || diag.Len() != 0 {
					t.Fatalf("retired profile started work: code=%d called=%v stderr=%s", code, called, diag.String())
				}
				end := assertEnd(t, decodeEvents(t, out.String()), "retired_profile")
				selected, _ := selectProfile(name)
				if end["profile"] != selected.id || end["error"] != errRetiredAuthProfile.Error() {
					t.Fatalf("retirement lost profile identity or explanation: %v", end)
				}
			}
			// Internal callers also cannot bypass CLI validation to revive a
			// historical profile. No client is needed to reject the run.
			selected, _ := selectProfile(name)
			var direct bytes.Buffer
			if code := runAgent(context.Background(), defaultInput(worker.server.URL), nil, config.Resolved{}, "unused", &emitter{out: &direct}, selected); code != 2 {
				t.Fatalf("internal runner accepted retired profile: %s", direct.String())
			}
			assertEnd(t, decodeEvents(t, direct.String()), "retired_profile")
			checks, actions, cancels := worker.counts()
			if checks != 0 || actions != 0 || cancels != 0 {
				t.Fatalf("retired profile contacted worker: %d/%d/%d", checks, actions, cancels)
			}
		})
	}
}

func TestProfilesKeepDefaultPromptAndEndMetadataFrozen(t *testing.T) {
	baseline := "9abcefdd1e931ba0b19286c77b032587d5a02f8122bdbc5bfe7ecbd436a3ca95"
	selected, err := selectProfile("browser")
	if err != nil || selected.id != "pogo-agent-browser-only" || selected.prompt != systemPrompt || selected.auth {
		t.Fatal(selected, err)
	}
	end := endFor(config.Resolved{Model: "fixture-model"})
	if end.Profile != "pogo-agent-browser-only" || end.SystemPromptSHA256 != baseline {
		t.Fatal(end)
	}
	raw, _ := json.Marshal(end)
	if strings.Contains(string(raw), "outcome") {
		t.Fatal("ordinary benchmark acquired auth-only fields")
	}
	for _, required := range []string{"synthetic", "human intervention", "Never replay", "auth_status", "api_read", "Do not export credentials", "Do not perform or simulate human approval"} {
		if !strings.Contains(authSystemPrompt, required) {
			t.Errorf("auth guidance lacks %q", required)
		}
	}
}

func TestInvalidProfileRejectedBeforeModelOrInput(t *testing.T) {
	called := false
	var out, diag bytes.Buffer
	code := runCLI(context.Background(), []string{"--profile", "invalid-profile-sensitive-string"}, strings.NewReader("not JSON"), &out, &diag, func(string, string) (llm.Client, config.Resolved, error) {
		called = true
		return nil, config.Resolved{}, nil
	})
	if code != 2 || called || diag.Len() != 0 {
		t.Fatal(code, called, diag.String())
	}
	end := assertEnd(t, decodeEvents(t, out.String()), "invalid_input")
	if end["error"] != "unknown evaluation profile; use browser, general-web, browser-use-v2, auth or auth-auto" || strings.Contains(out.String(), "invalid-profile-sensitive-string") {
		t.Fatal(out.String())
	}
}

func TestAuthOutcomeRequiresOneStrictObjectAndDoesNotInventSuccess(t *testing.T) {
	for _, text := range []string{`{"status":"completed","reason":"api_read_complete","data":{}}`, `{"status":"needs_human","reason":"mfa_required"}`, `{"status":"blocked","reason":"invalid_credentials"}`} {
		if _, err := parseAuthOutcome(text); err != nil {
			t.Errorf("valid outcome rejected: %v", err)
		}
	}
	for _, text := range []string{"done", "```json\n{}\n```", `null`, `{"status":"success","reason":"done"}`, `{"status":"completed","reason":""}`, `{"status":"completed","reason":"done","data":[]}`, `{"status":"completed","reason":"done","data":null}`, `{"status":"completed","reason":"done","expected":true}`, `{"status":"completed","reason":"done"}{}`, strings.Repeat("x", 65537)} {
		if _, err := parseAuthOutcome(text); err == nil {
			t.Errorf("invalid outcome accepted: %.120s", text)
		}
	}
}

func TestAuthHandoffAndBlockedOutcomesDoNotClaimTaskCompletion(t *testing.T) {
	for _, status := range []string{"needs_human", "blocked"} {
		outcome, err := parseAuthOutcome(`{"status":"` + status + `","reason":"authorization_required"}`)
		if err != nil || outcome.Status != status || outcome.Data != nil {
			t.Fatalf("offline parsing changed historical handoff: %+v / %v", outcome, err)
		}
	}
}

func TestAuthV2PromptExamplesUseTheSameStrictOutcomeContract(t *testing.T) {
	if !strings.Contains(authSystemPrompt, "Authentication output protocol: auth-output-v2.") {
		t.Fatal("the auth reporting protocol must identify its prompt revision")
	}
	if !strings.Contains(authSystemPrompt, "the JSON object itself is the request for human action") {
		t.Fatal("handoff guidance must not require prose around the structured result")
	}
	reasons := map[string]bool{}
	for _, line := range strings.Split(authSystemPrompt, "\n") {
		if !strings.HasPrefix(line, `{"status":`) {
			continue
		}
		outcome, err := parseAuthOutcome(line)
		if err != nil {
			t.Fatalf("invalid JSON example in auth prompt: %v", err)
		}
		reasons[outcome.Reason] = true
	}
	for _, reason := range []string{"authorization_required", "login_required", "mfa_required", "invalid_credentials", "api_read_complete"} {
		if !reasons[reason] {
			t.Errorf("missing reporting example for %s", reason)
		}
	}
	for _, answer := range []string{
		"The login was rejected.\n\n" + `{"status":"blocked","reason":"invalid_credentials"}`,
		"```json\n" + `{"status":"needs_human","reason":"authorization_required"}` + "\n```",
	} {
		if _, err := parseAuthOutcome(answer); err == nil {
			t.Fatal("prompt revision must not relax strict answer validation")
		}
	}
}

func TestArchivedAuthOutcomeEmissionRedactsSecretsInKeysValuesAndCollisions(t *testing.T) {
	const headerSecret = "synthetic-private-header-alpha"
	const anotherSecret = "synthetic-private-header-beta"
	answer, err := json.Marshal(map[string]any{
		"status": "completed", "reason": "api_read_complete",
		"data": map[string]any{
			headerSecret: map[string]any{"nested": []any{anotherSecret, map[string]any{anotherSecret: "prefix " + headerSecret}}},
			"collision": map[string]any{
				headerSecret:  map[string]any{"private": anotherSecret},
				anotherSecret: map[string]any{"private": headerSecret},
				"[redacted]":  map[string]any{"private": headerSecret},
			},
			"ordinary": "retained",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := parseAuthOutcome(string(answer))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	e := emitter{out: &out, secrets: []string{headerSecret, anotherSecret}}
	selected, _ := selectProfile("auth")
	archived := endForProfile(config.Resolved{Model: "archived-model"}, selected)
	archived.EndReason, archived.Answer, archived.Outcome = "completed", string(answer), outcome
	e.emit(archived)
	if e.err != nil {
		t.Fatal(e.err)
	}
	for _, secret := range []string{headerSecret, anotherSecret} {
		if strings.Contains(out.String(), secret) {
			t.Fatal("known provider header credential was written to the event stream")
		}
	}
	end := assertEnd(t, decodeEvents(t, out.String()), "completed")
	data := end["outcome"].(map[string]any)["data"].(map[string]any)
	if data["ordinary"] != "retained" {
		t.Fatal("redaction removed unrelated output")
	}
	nested := data["[redacted]"].(map[string]any)["nested"].([]any)
	if nested[0] != "[redacted]" || nested[1].(map[string]any)["[redacted]"] != "prefix [redacted]" {
		t.Fatal("nested private keys or values were not redacted")
	}
	collision := data["collision"].(map[string]any)
	if len(collision) != 1 || collision["[redacted]"] != "[redacted key collision]" {
		t.Fatal("redacted-key collisions must omit ambiguous values deterministically")
	}
}

func TestAuthAutoProfileKeepsHistoricalAuthPromptFrozen(t *testing.T) {
	selected, err := selectProfile("auth")
	hash := sha256.Sum256([]byte(selected.prompt))
	if err != nil || selected.id != authProfile || !selected.auth || hex.EncodeToString(hash[:]) != "f40aa9f8fe8569387a890410536ce86287258d27d7fe0550e3b61cba10229a85" {
		t.Fatal("historical manual auth profile changed")
	}
	automatic, err := selectProfile("auth-auto")
	if err != nil || automatic.id != "pogo-agent-browser-auth-auto-v2" || automatic.prompt != authAutoSystemPrompt || !automatic.auth {
		t.Fatal("automatic profile not independent")
	}
	for _, required := range []string{"automatically reuses", "Credential presence is not proof", "AUTH_CREDENTIAL_SELECTION_REQUIRED", "storage_key", "No login", "HTTP 401", "HTTP 403", "Do not automatically retry", "Never replay", "Do not export credentials"} {
		if !strings.Contains(automatic.prompt, required) {
			t.Errorf("automatic profile lacks %q", required)
		}
	}
	reasons := map[string]bool{}
	for _, line := range strings.Split(automatic.prompt, "\n") {
		if !strings.HasPrefix(line, `{"status":`) {
			continue
		}
		outcome, err := parseAuthOutcome(line)
		if err != nil {
			t.Fatalf("invalid auto output example: %v", err)
		}
		reasons[outcome.Reason] = true
	}
	for _, reason := range []string{"login_required", "mfa_required", "invalid_credentials", "site_access_denied", "api_read_complete"} {
		if !reasons[reason] {
			t.Errorf("missing auto reason: %s", reason)
		}
	}
	if reasons["authorization_required"] || strings.Contains(automatic.prompt, "credential_ref") {
		t.Fatal("automatic profile retained manual grant contract")
	}
}
