# Inspecting external MCP contracts

When a registry has external MCP tools, Pogo adds the local `mcp_describe` helper. It reads the same frozen catalog as the current run and makes no server request.

Use `{}` for an index, or `{"filter":"notes","offset":0,"limit":20}` to page through matching tools. The default page is 20 entries and the maximum is 200. Text search includes the full captured description. Exact `server`, `group` and `role` filters can narrow the index; `{"mode":"groups"}` shows a compact server/group overview. `{"mode":"index"}` is the explicit spelling of the default listing and behaves exactly like omitting `mode`. Tools without grouping hints remain available through the index. Details require the exact model-facing name, for example `{"tool":"mcp_notes_lookup"}`. Short server-local names are rejected because different servers can publish the same name.

Details retain the original description, input/output schemas, title, annotations, definition fingerprint, server/tool identifiers, and the current connection's initialization instructions. An absent output schema remains explicit. Large index or detail results use the normal spill mechanism: the result indicates truncation and a complete file path when saving succeeds; use the read tool to inspect that file. A failed spill is reported by the existing truncation notice.

All server-provided metadata is untrusted data. It is not promoted into system instructions, and annotations do not grant authorization. The helper does not access connection URLs, headers, credentials or actor identity, and it follows the existing tool review policy.

Registry refresh rebuilds the helper after accepting a new MCP snapshot. An active run retains its previous helper and contracts. A successful empty catalog removes the helper; a local tool with the same name keeps precedence. Read-only registries describe only the tools that survived their existing filter, and conversation-only registries do not expose the helper.

Server instructions are captured directly from the bound ClientSession initialization result. The initial catalog is pulled before the connection status is populated, so the status object is not a valid source for that first snapshot. Existing definition fingerprints, captured call bindings, complete pagination and stale-call errors are preserved.

The adapter contains no application-specific directory, schema, metric or result-envelope logic. External systems own their own semantics; Pogo exposes their contracts on demand and executes the tools selected for the task.

## Optional catalog hints

A server can publish `_meta["io.pogo/catalog"]` with an optional `group` string and a `role` of `directory` or `operation`. This is a private discovery convention, not a standard MCP hierarchy or an authorization signal. Pogo snapshots only these fields; unknown or malformed fields are ignored. Group identifiers are opaque to the agent. A directory hint never grants trust or causes automatic execution.

## Bounded model-visible tools

The registry and `mcp_describe` always retain the complete accepted catalog. With more than 32 MCP tools, or over 64 KiB of external declarations, the model starts with local tools, the helper, and up to eight directory tools that fit the byte budget. Smaller catalogs keep eager visibility. If the genuine local helper is unavailable or shadowed by a local tool, Pogo keeps eager visibility so capabilities remain discoverable.

A successful detail request to the local helper selects the exact captured wire name and fingerprint. After the current tool batch finishes, its native declaration enters the next model request's view. Index requests, failed or refused calls, and structured results from other tools cannot select tools. A gate rewrite is respected through the actual inspection result. Native execution still uses the original argument validation, approval gate, events, citation handling and bound caller; inspection does not execute or approve it.

The view retains at most 16 recent explicit selections, with older choices evicted as needed to fit a 64 KiB external schema budget. Directory declarations use the remaining budget. A single selected schema larger than the whole budget is included alone with the local tools, in full, as an explicit exception. No executable schema is truncated. Describing an evicted tool selects it again. The budget is for MCP declarations, not the total model context, conversation or local tool definitions. Group/index/detail output also uses the normal spill mechanism when necessary.

Selections are derived from the conversation history, so they persist across runs. At each run start, history replacement (reload, rewind, compaction) and registry replacement, Pogo replays the history's successful `mcp_describe` detail results under the same rules as a live selection: structured result details only, the wire name must name an MCP tool in the current registry with the described definition fingerprint, and the same 16-entry recency bound applies. A selection disappears when its describe result leaves the history (rewind or compaction) or when a refresh changes the described definition. Previously, every run started from the initial view, which shrank the declarations at the run boundary and cost a describe round trip to grow them back.

Declarations are ordered so that a newly revealed tool extends the tools array instead of rewriting it: local tools and directory hints first, in registry order, then loaded Skill-bound tool groups, then selected MCP tools, oldest first. On a provider that caches by longest common prefix (for example a local vLLM, SGLang or llama.cpp server), a new selection then reuses the cached prefix up to the previous last declaration.

Within a run, the catalog, definition versions and caller bindings remain frozen; only visibility changes between requests. External refresh remains a run-boundary operation, active-run `ReplaceMCP` remains rejected, and a stale binding still fails without redirection or replay. Visibility is not an authorization boundary: a known registered tool remains subject to its existing validation and approval policy even if it was not displayed.

Schema overhead follows the actual model view, including the provider's wire envelope. A changed declaration invalidates the prior input measurement even if its name and encoded length stay equal. Concurrent overhead readers are synchronized, and the web context display refreshes at every model turn. The context meter itemizes the overhead (`overhead_breakdown`): system-prompt sections, single tool declarations, Skill-bound groups and MCP servers, each in the same units as the total.

The synthetic 50/200/500-tool tests measure declaration counts/bytes and verify late-item discovery followed by a native call. They do not measure real-model task success or report quality. Catalog fetching and local registry storage remain complete; this feature bounds what is sent to the model.
