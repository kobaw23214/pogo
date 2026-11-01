# Conversation-first work

The conversation shows progress, tool activity, answers, citations and deliverables.
Running output and thinking previews remain visible as the task proceeds.
This implementation covers reliable follow-ups, live work display and resumable
subagent contexts. It does not add mid-tool preemption or a parallel task scheduler.

## Follow-ups

While a normal Web or Studio conversation is running, sending prose saves a
follow-up in its durable inbox. The composer shows the receipt immediately after
acceptance. A queued message can be edited or withdrawn; **Steer now** promotes it
into the existing run at the next valid tool-result boundary. It does not cancel
the current tool, remove the original objective or imply the model already read
the message. Once delivery is underway, editing and withdrawal are refused.

After a successful run, ordinary queued messages start in order. Cancellation,
errors, budget termination and server recovery leave outstanding messages visible
for explicit continuation. Attachments, explicit skill invocations and delegated
embed restrictions retain their existing support boundaries.

The `/api/sessions/{sid}/control` actions are:

| Action | Fields | Meaning |
| --- | --- | --- |
| `inbox_send` | `id`, `prompt`, `mode: queued/steer`, optional `expected_run_id` | Persist an idempotent submission. |
| `inbox_steer` | `id`, `revision` | Deliver at the existing boundary, or start normally if idle. |
| `inbox_edit` | `id`, `revision`, `prompt` | Update a still-queued message. |
| `inbox_withdraw` | `id`, `revision` | Retain the withdrawal receipt without delivering the text. |

Responses contain `{accepted, item}`; `inbox_updated` events carry `item`, and
snapshots contain `inbox`. Items have stable IDs, monotonically increasing
revisions and queued/steering/delivered/withdrawn states. `delivered` means recorded
in the conversation before the next model call, not proof of semantic acceptance
or task completion. Clients must ignore stale HTTP receipts after newer events.
IDs identify submissions, never permissions; every operation uses session access
checks. Sidecar persistence is bounded and checked before acknowledging success.

Delivered user messages retain `inbox_id`, `run_id` and `steering` in replay.
Steering stays in the original exchange, with continuous turn numbering and its
previously consulted sources. A queued message starting a new run is an ordinary
new exchange. Metadata is local and is not sent as provider-specific fields.

## Live work display

The activity-disclosure redesign was reverted. Running turns show individual
calls with their live output and results, and thinking uses the existing stream
tail preview with full text available on demand. Tool cards, MCP and Skill
results retain their original presentation. Calls do not regroup or disappear
when another result arrives.

Completed exchanges use the original work-log presentation with their answer
outside the fold. Exchanges containing steering messages retain chronological
message rendering so follow-ups and user-facing prose remain readable. Pending
human decisions force the work area open. Retained child identity and context
are independent of these presentation controls.

## Retained subagents

The existing `subagent` tool accepts `action: spawn` (the default), `continue`,
`list` and `archive`. `continue` needs an `agent_id` and a new `task`; `archive`
needs an `agent_id`. Spawn and run results return the stable `agent_id` alongside
the current `run_id` and transcript. Listing and archiving do not call a model.

Logical agents are scoped to their owner and parent session. A completed agent
is idle, with its transcript retained; no process or model request remains active.
Continuation starts a new process with that history, keeps the original model and
mode, checks the current permission boundary and takes an exclusive transcript
lease. Interrupted records require explicit continuation. Existing concurrency,
read-only and browser-loan restrictions still apply. Archived contexts keep their
history but are unavailable for continuation.

Restored runs report incremental usage. Background completion is recorded once
per run, so old usage is not repeatedly charged to the parent's accounting.
Historical browser references and earlier approvals are not renewed by retaining
context. Full history remains available even when existing context management
compresses the model input.

The read-only endpoint
`GET /api/sessions/{parentSid}/subagents/{agentID}/transcript` authenticates the
parent and resolves the child through its registry. It never promotes a child
into a runnable top-level session. New child sheets keep both parent and agent
identity; old transcript links retain their existing compatibility path.
