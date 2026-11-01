// Cross-SDK smoke: drive pogo's MCP server with the OFFICIAL TypeScript SDK —
// the Go-side tests (web/mcp_test.go, cmd/mcp-probe) use the same SDK on both
// ends, and a protocol layer whose point is interop earns nothing from that.
//
// Prereqs:  npm install  (in this directory — pulls @modelcontextprotocol/sdk)
// Run:      POGO_CONFIG=... POGO_MCP=1 POGO_WEB_TOKEN=t pogo -web -listen 127.0.0.1:7777
//           node smoke.mjs [baseUrl] [token]
//
// Exercises: initialize → tools/list (agent_* + annotations + outputSchema
// present) → tools/call on an unknown task (execution error, not protocol
// error) → a real submit → status → cancel. A submit against a deployment
// without a live model still returns a task; the run fails, which is fine —
// the smoke is about the wire, not the answer.
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

const base = process.argv[2] ?? "http://127.0.0.1:7777";
const token = process.argv[3] ?? "t";

const client = new Client({ name: "ts-smoke", version: "0.1.0" });
await client.connect(
  new StreamableHTTPClientTransport(new URL(`${base}/mcp`), {
    requestInit: { headers: { Authorization: `Bearer ${token}` } },
  }),
);

const { tools } = await client.listTools();
const names = new Set(tools.map((t) => t.name));
for (const want of [
  "agent_submit_task", "agent_task_status", "agent_task_result",
  "agent_task_cancel", "agent_task_wait", "agent_task_steer",
  "agent_task_approve", "agent_task_list", "agent_task_file",
]) {
  if (!names.has(want)) throw new Error(`tools/list missing ${want}`);
}
const status = tools.find((t) => t.name === "agent_task_status");
if (!status.outputSchema) throw new Error("agent_task_status has no outputSchema");
if (status.annotations?.readOnlyHint !== true) throw new Error("status should be readOnly");
console.log(`tools/list ok: ${tools.length} tools`);

// Unknown task: an execution error (isError content), never a protocol throw.
const unknown = await client.callTool({ name: "agent_task_status", arguments: { task_id: "nope" } });
if (!unknown.isError) throw new Error("unknown task should be isError");
console.log("unknown-task error semantics ok");

const sub = await client.callTool({
  name: "agent_submit_task",
  arguments: { prompt: "Reply with exactly: ts-smoke-ok", client_ref: "ts-smoke-1" },
});
if (sub.isError) throw new Error(`submit: ${JSON.stringify(sub.content)}`);
const taskId = sub.structuredContent?.task_id;
if (!taskId) throw new Error("submit returned no structuredContent.task_id");
console.log(`submit ok: ${taskId}`);

const st = await client.callTool({ name: "agent_task_status", arguments: { task_id: taskId } });
const s = st.structuredContent?.status;
if (!["working", "input_required", "completed", "failed", "cancelled"].includes(s)) {
  throw new Error(`unexpected status ${s}`);
}
console.log(`status ok: ${s}`);

// Idempotency over the wire: the replayed ref dedups.
const again = await client.callTool({
  name: "agent_submit_task",
  arguments: { prompt: "Reply with exactly: ts-smoke-ok", client_ref: "ts-smoke-1" },
});
if (again.structuredContent?.deduplicated !== true) throw new Error("client_ref replay did not dedup");
console.log("client_ref dedup ok");

await client.callTool({ name: "agent_task_cancel", arguments: { task_id: taskId } });
console.log("cancel ok");
await client.close();
console.log("TS-SMOKE-PASS");
