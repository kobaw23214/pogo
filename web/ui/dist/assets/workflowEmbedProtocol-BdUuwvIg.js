function e(e){return/^\/embed\/workflows\/([A-Za-z0-9_-]+)\/?$/.exec(e)?.[1]??null}function t(e){if(!e)return null;try{let t=new URL(e);return![`http:`,`https:`].includes(t.protocol)||t.username||t.password||t.pathname!==`/`||t.search||t.hash||t.origin===`null`?null:t.origin}catch{return null}}function n(e,t,n,r,i){if(!t||e.source!==t||e.origin!==n)return null;let a=e.data;return!a||a.type!==`pogo.workflow.launch`||a.protocol!==1||a.publication_id!==r||a.request_id!==i||typeof a.code!=`string`||!a.code||a.code.length>512?null:a.code}function r(e,t){let n=new URL(e);return t?n.searchParams.set(`run`,t):n.searchParams.delete(`run`),n.searchParams.delete(`token`),n.hash=``,n.href}function i(e,t,n){let r=new URL(`/embed/workflows/${encodeURIComponent(t)}`,e);r.searchParams.set(`parent_origin`,n);let i=new URL(e).origin;return`<iframe id="pogo-workflow" title="Workflow" style="width:100%;height:720px;border:0"></iframe>
<script>
const frame = document.getElementById("pogo-workflow");
const hostUrl = new URL(window.location.href);
let runId = hostUrl.searchParams.get("pogo_run") || "";
const frameUrl = new URL(${JSON.stringify(r.href)});
if (runId) frameUrl.searchParams.set("run", runId);
window.addEventListener("message", async (event) => {
  if (event.origin !== ${JSON.stringify(i)} || event.source !== frame.contentWindow) return;
  const message = event.data;
  if (message?.protocol !== 1 || message.publication_id !== ${JSON.stringify(t)}) return;
  if (message.type === "pogo.workflow.ready") {
    // Your backend verifies the current user and access to run_id, then asks
    // Pogo for a launch code. Pogo checks run access again. Keep the secret
    // on your backend; a browser-supplied run_id is never authorization.
    const response = await fetch("/api/my-workflow-launch", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ publication_id: message.publication_id, run_id: runId || undefined }) });
    if (!response.ok) return;
    const { launch_code } = await response.json();
    frame.contentWindow.postMessage({ type: "pogo.workflow.launch", protocol: 1, publication_id: message.publication_id, request_id: message.request_id, code: launch_code }, ${JSON.stringify(i)});
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
<\/script>`}export{t as a,n as i,e as n,r,i as t};