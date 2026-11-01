export const WORKFLOW_EMBED_PROTOCOL = 1;

export function workflowEmbedId(path: string): string | null {
  return /^\/embed\/workflows\/([A-Za-z0-9_-]+)\/?$/.exec(path)?.[1] ?? null;
}

export function workflowParentOrigin(value: string | null): string | null {
  if (!value) return null;
  try {
    const url = new URL(value);
    if (!["http:", "https:"].includes(url.protocol) || url.username || url.password || url.pathname !== "/" || url.search || url.hash || url.origin === "null") return null;
    return url.origin;
  } catch { return null; }
}

export function workflowLaunchMessage(
  event: Pick<MessageEvent, "source" | "origin" | "data">,
  parent: MessageEventSource | null,
  origin: string,
  publicationId: string,
  requestId: string,
): string | null {
  if (!parent || event.source !== parent || event.origin !== origin) return null;
  const data = event.data as Record<string, unknown> | null;
  if (!data || data.type !== "pogo.workflow.launch" || data.protocol !== WORKFLOW_EMBED_PROTOCOL || data.publication_id !== publicationId || data.request_id !== requestId || typeof data.code !== "string" || !data.code || data.code.length > 512) return null;
  return data.code;
}

export function workflowEmbedLocation(href: string, runId: string | null): string {
  const url = new URL(href);
  if (runId) url.searchParams.set("run", runId);
  else url.searchParams.delete("run");
  // A host must send credentials only through the checked message channel.
  url.searchParams.delete("token");
  url.hash = "";
  return url.href;
}

export function workflowEmbedExample(base: string, publicationId: string, parentOrigin: string): string {
  const frame = new URL(`/embed/workflows/${encodeURIComponent(publicationId)}`, base);
  frame.searchParams.set("parent_origin", parentOrigin);
  const origin = new URL(base).origin;
  return `<iframe id="pogo-workflow" title="Workflow" style="width:100%;height:720px;border:0"></iframe>
<script>
const frame = document.getElementById("pogo-workflow");
const hostUrl = new URL(window.location.href);
let runId = hostUrl.searchParams.get("pogo_run") || "";
const frameUrl = new URL(${JSON.stringify(frame.href)});
if (runId) frameUrl.searchParams.set("run", runId);
window.addEventListener("message", async (event) => {
  if (event.origin !== ${JSON.stringify(origin)} || event.source !== frame.contentWindow) return;
  const message = event.data;
  if (message?.protocol !== 1 || message.publication_id !== ${JSON.stringify(publicationId)}) return;
  if (message.type === "pogo.workflow.ready") {
    // Your backend verifies the current user and access to run_id, then asks
    // Pogo for a launch code. Pogo checks run access again. Keep the secret
    // on your backend; a browser-supplied run_id is never authorization.
    const response = await fetch("/api/my-workflow-launch", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ publication_id: message.publication_id, run_id: runId || undefined }) });
    if (!response.ok) return;
    const { launch_code } = await response.json();
    frame.contentWindow.postMessage({ type: "pogo.workflow.launch", protocol: 1, publication_id: message.publication_id, request_id: message.request_id, code: launch_code }, ${JSON.stringify(origin)});
  }
  if (message.type === "pogo.workflow.run" && typeof message.run_id === "string" && /^run_[A-Za-z0-9_-]+$/.test(message.run_id)) {
    runId = message.run_id;
    const currentUrl = new URL(window.location.href);
    currentUrl.searchParams.set("pogo_run", runId);
    window.history.replaceState(window.history.state, "", currentUrl.href);
    // Optional: also save runId/status with your backend's business record.
  }
});
// Register the message listener before loading the iframe. A host refresh
// resumes the recorded Run and never starts another Run automatically.
frame.src = frameUrl.href;
</script>`;
}
