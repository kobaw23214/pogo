package web

import (
	"strings"

	"github.com/yosukeno/pogo/skills"
)

// uiSectionName mirrors the skill name in skills/builtin/web-drawing; the two
// are one capability and must move together.
const uiSectionDrawingSkill = "web-drawing"
const uiSectionDevelopmentSkill = "web-development"

// uiCapabilitiesSection tells the model what the answer surface can render.
// These are Web UI facts whether or not a routing skill loaded; the skill only
// decides when a richer representation is clearer than prose. Terminal sessions
// never see this section because their fences remain code.
func uiCapabilitiesSection(list []skills.Skill) string {
	var b strings.Builder
	b.WriteString("The answers render in a web UI that draws ```mermaid diagrams and ```echarts interactive charts from JSON options. " +
		"It also renders math ($...$, $$...$$ and ```math), data grids (```csv, ```table and ```json-table), " +
		"and rich Markdown including [TOC], [^1] footnotes, task lists, > [!NOTE] callouts, :::details and diff.\n" +
		"Use ![alt](workspace/path.png) to inline an existing workspace image. Never run scripts or create Base64/HTML embeds just to display an image.\n" +
		"For an interactive HTML artifact, save a real .html or .htm workspace file, then use a ```html-preview fence containing only JSON like {\"path\":\"artifacts/app.html\",\"title\":\"App\"}. Ordinary html fences remain source code.\n" +
		"For static websites using project-local HTML, CSS, JavaScript, modules, JSON and media, use this built-in HTML preview. Do not start Python or Node HTTP servers just to display them. The preview serves local assets and refreshes after completed edits; its temporary URL is not a permanent website deployment. External dependencies and APIs, form submissions, subframes and workers are blocked.\n" +
		"A backend, SSR, server-side API or WebSocket server needs a managed Web service capability. Use it only when it is actually listed among the available tools, following its declared contract. If unavailable, explain the limitation; do not substitute an unmanaged background server, guess a localhost URL, or change container port mappings.\n")
	b.WriteString("Use only the advertised standard-library Python or Node.js web_service profiles, without dependency installation or build. Its application cannot initiate TCP, UDP or Unix connections, including loopback; browser requests to its own API are supported. start acceptance is not readiness: inspect status, then open only when ready. Source changes need explicit restart. Reading status/logs/open does not renew the service; use the platform default lifetime unless the user specifies one. The website address remains valid for that service lifetime, with no separate preview-link expiry. Prefer the returned owner service_url for a service card. HTTP readiness and browser interaction are separate evidence; neither is a production deployment.\n")
	b.WriteString("To embed a managed website directly in the conversation, use a ```web-preview fence containing only JSON like {\"service_id\":\"ws_<actual service ID>\",\"title\":\"App\"}. Copy the exact service_id from this conversation's real web_service result; title is optional display fallback, not a rename. Never put a preview_url, browser_url, service_url, session_id or raw HTML in this fence. The client checks current conversation ownership and obtains the lifecycle URL itself. Embed only after status is ready; viewing an embed never starts, restarts or renews a website. Stopped, expired, deleted or unavailable services remain status cards, and public/shared copies do not load private websites.\n")
	b.WriteString("Both HTML Preview and managed websites require project-local images: external image URLs are blocked by their CSP. When real photos are requested, find the actual source images and download them into the project before starting or restarting it, using available authorized browser downloads or development commands when network access is permitted. Development command networking and managed-app networking are separate capabilities. Serve local image files with binary responses and correct MIME types, using relative URLs; an app that only serves HTML/CSS/JS also needs image routes. Check actual image loading and pixels, not just img/alt presence or the entry health check. Do not claim downloads are blocked without a real refusal, or substitute SVG illustrations for requested photos while claiming completion.\n")
	b.WriteString("Managed service URLs have distinct roles: service_url opens the authenticated owner service card; preview_url is for the current client's iframe preview; the native browser tool must use only a non-null browser_url. If browser_url is null, do not fall back to preview_url or service_url for native browser navigation, rewrite their host or port, or guess an internal address. Keep the client iframe preview available and report that the native route is unverified. After the platform fixes and verifies the route, open again to obtain a non-null browser_url before native browser validation, subject to the browser tool's existing availability and task authorization. An unverified native route does not imply the client website is unavailable.\n")
	b.WriteString("An administrator may explicitly enable preview-mode=path for http://LAN-IP:19770 without a domain or certificate. Use only the returned preview_url under /_pogo/web/<48hex>/. In managed apps, browser assets and APIs must use relative URLs like ./assets/app.js and ./api/counter; /api/counter is root-absolute, leaves the grant prefix, and is blocked by the preview CSP. Build WebSocket URLs from the current pathname's grant prefix and host, using ws: for HTTP or wss: for HTTPS. The gateway strips only the grant prefix; server routes remain / and /api/counter. Do not rely on body rewriting, globally patch fetch/XHR/WebSocket, or remove sandboxing or add allow-same-origin. Static sites still use HTML Preview; domain preview mode remains supported.\n")
	if _, ok := skills.Find(list, uiSectionDevelopmentSkill); ok {
		b.WriteString("For website development, read the " + uiSectionDevelopmentSkill + " skill to choose static preview or managed runtime. Loading a Skill grants no tools or deployment permission.\n")
	}
	if _, ok := skills.Find(list, uiSectionDrawingSkill); ok {
		b.WriteString("When a diagram, chart, formula, data table or long structured document would communicate better than prose, read the " +
			uiSectionDrawingSkill + " skill first and follow its routing; do not ask the user which format to use. Ordinary code examples remain fenced code.")
	} else {
		b.WriteString("Draw pictures only when the user asks for one. Use the other rich formats when they help; otherwise fenced blocks remain code.")
	}
	return b.String()
}
