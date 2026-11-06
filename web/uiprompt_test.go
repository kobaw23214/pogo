package web

import (
	"strings"
	"testing"

	"github.com/yosukeno/pogo/skills"
)

func TestUICapabilitiesSection(t *testing.T) {
	with := uiCapabilitiesSection([]skills.Skill{{Name: "web-drawing", Description: "draws"}})
	for _, want := range []string{"```mermaid", "```echarts", "```math", "```csv", "```table", "```json-table", "[TOC]", "[^1]", ":::details", "web-drawing skill", "do not ask the user", "![alt](workspace/path.png)", "```html-preview", "Ordinary html fences remain source code", "Never run scripts", "Base64/HTML"} {
		if !strings.Contains(with, want) {
			t.Errorf("section with the skill loaded is missing %q:\n%s", want, with)
		}
	}

	bare := uiCapabilitiesSection(nil)
	if strings.Contains(bare, "read the web-drawing") {
		t.Errorf("section without the skill must not tell the model to read it:\n%s", bare)
	}
	for _, want := range []string{"only when the user asks", "Use the other rich formats when they help", "```math", "```csv", "![alt](workspace/path.png)", "```html-preview", "Ordinary html fences remain source code", "Never run scripts"} {
		if !strings.Contains(bare, want) {
			t.Errorf("bare variant is missing %q:\n%s", want, bare)
		}
	}
}

func TestUICapabilitiesManagedWebsiteEmbedUsesServiceIdentity(t *testing.T) {
	for _, list := range [][]skills.Skill{nil, {{Name: "web-development"}}} {
		section := uiCapabilitiesSection(list)
		for _, want := range []string{"```web-preview", "exact service_id", "current conversation ownership", "title is optional display fallback, not a rename", "Never put a preview_url, browser_url, service_url, session_id or raw HTML", "never starts, restarts or renews", "public/shared copies do not load private websites"} {
			if !strings.Contains(section, want) {
				t.Errorf("managed website embed guidance is missing %q", want)
			}
		}
	}
}

func TestUICapabilitiesStaticWebsiteRoutingDoesNotDependOnDrawingSkill(t *testing.T) {
	for _, tt := range []struct {
		name   string
		skills []skills.Skill
	}{
		{name: "without_drawing_skill"},
		{name: "with_drawing_skill", skills: []skills.Skill{{Name: "web-drawing"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			section := uiCapabilitiesSection(tt.skills)
			// These deployment facts must remain in the host prompt even when the
			// optional visualization-routing skill was not selected for this Agent.
			for _, want := range []string{
				"static websites using project-local HTML, CSS, JavaScript, modules, JSON and media",
				"use this built-in HTML preview",
				"Do not start Python or Node HTTP servers just to display them",
				"temporary URL is not a permanent website deployment",
				"External dependencies and APIs, form submissions, subframes and workers are blocked",
				"backend, SSR, server-side API or WebSocket server",
				"only when it is actually listed among the available tools",
				"If unavailable, explain the limitation",
				"do not substitute an unmanaged background server",
				"guess a localhost URL",
				"change container port mappings",
			} {
				if !strings.Contains(section, want) {
					t.Errorf("website routing is missing %q:\n%s", want, section)
				}
			}
		})
	}
}

func TestUICapabilitiesDevelopmentSkillIsOptionalGuidance(t *testing.T) {
	for _, selected := range []bool{false, true} {
		var list []skills.Skill
		if selected {
			list = []skills.Skill{{Name: "web-development"}}
		}
		section := uiCapabilitiesSection(list)
		if strings.Contains(section, "read the web-development skill") != selected {
			t.Fatalf("selected=%v: optional Skill discovery mismatches prompt", selected)
		}
		// Runtime facts remain available even when the routing Skill is omitted;
		// discovery must not imply execution or production-release authority.
		for _, invariant := range []string{
			"advertised standard-library Python or Node.js",
			"start acceptance is not readiness",
			"open only when ready",
			"explicit restart",
			"Reading status/logs/open does not renew",
			"returned owner service_url",
			"neither is a production deployment",
			"project-local images",
			"Development command networking and managed-app networking are separate capabilities",
			"binary responses and correct MIME types",
			"service_url opens the authenticated owner service card",
			"preview_url is for the current client's iframe preview",
			"the native browser tool must use only a non-null browser_url",
			"If browser_url is null, do not fall back to preview_url or service_url for native browser navigation",
			"rewrite their host or port, or guess an internal address",
			"Keep the client iframe preview available",
			"open again to obtain a non-null browser_url before native browser validation",
			"existing availability and task authorization",
			"An unverified native route does not imply the client website is unavailable",
			"preview-mode=path",
			"http://LAN-IP:19770 without a domain or certificate",
			"/_pogo/web/<48hex>/",
			"relative URLs like ./assets/app.js and ./api/counter",
			"/api/counter is root-absolute",
			"blocked by the preview CSP",
			"current pathname's grant prefix and host",
			"ws: for HTTP or wss: for HTTPS",
			"server routes remain / and /api/counter",
			"globally patch fetch/XHR/WebSocket",
			"add allow-same-origin",
			"Static sites still use HTML Preview",
			"domain preview mode remains supported",
		} {
			if !strings.Contains(section, invariant) {
				t.Errorf("selected=%v: missing runtime boundary %q", selected, invariant)
			}
		}
		for _, obsolete := range []string{"standard-library Python only", "the default service lease is two hours", "a preview grant lasts at most 30 minutes"} {
			if strings.Contains(section, obsolete) {
				t.Errorf("selected=%v: obsolete runtime claim %q", selected, obsolete)
			}
		}
	}
}
