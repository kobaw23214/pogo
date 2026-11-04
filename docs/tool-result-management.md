# Tool result storage and model views

Executed agent calls pass through `Registry.PrepareResult` before their text is
sent to the model. Failure status is independent of text: an error no longer
replaces an already bounded observation with an unbounded `err.Error()`.
UI details and image-reference delivery retain their existing contracts.

## Saved observations

For action/execution tools, the host saves an immutable observation and returns
an `R`-prefixed SHA-256 result ID only after the write, sync, close and rename
succeed. The ID identifies content, not a fresh observation time. The original
success/error status is retained separately from whether reading it succeeds.

Web sessions bind storage to owner and conversation; CLI sessions bind it to the
transcript path; workflow nodes bind it to run and node. Registries without a
durable identity receive an ephemeral private namespace. `WithMCP` retains the
same store across catalog refreshes. A result reader never searches other
namespaces. Result IDs alone do not grant cross-session access.

The base remains `Options.SpillDir`, `POGO_SPILL_DIR`, or `~/.pogo/spill`.
New artifacts live under `scopes/<scope hash>/`, with JSON observation records
in `results/result-<content hash>.log`. Storage usage and pruning traverse these
managed layouts without following symlinks. Ordinary retention remains seven
days by default; zero disables automatic cleanup. Valid saves/reads also obtain
a one-hour activity lease. Explicit retention defaults to 30 days and is capped
at 90 days. Clearing explicit retention does not delete the original or revoke
an active lease. Expired or unavailable IDs produce an explicit error; the host
does not replay the original action.

Mutable catalog, source-discovery and expiry metadata live in a bounded
`results/.metadata` sidecar, outside the content hash. Cross-process locks
coordinate save/read/retain with pruning on Unix and Windows. Before inference,
the agent temporarily protects up to 1024 recent/trusted checkpoint references;
successful refreshes are throttled to ten minutes, failed attempts to one minute.
Missing records are never recreated. These leases protect an active working set,
not every historic result forever: use explicit retention for important results
needed beyond normal TTL. A directory listing alone never renews all leases.

### Deleting a Web conversation

Deleting a Web conversation also removes that conversation's own
`scopes/<scope hash>/` directory, including raw tool output, stored observations
and lifecycle metadata. The scope uses the same trusted owner and conversation
identity as tool registration. It is not selected from a transcript's paths or
from model arguments. Unloaded conversations resolve ownership from the catalog;
an ownership lookup failure must not fall back to another user's namespace.

Running or compacting conversations and unsaved Office work retain the existing
deletion guards. After the conversation and its managed background work have
stopped, explicit deletion clears its dedicated Spill directory regardless of
TTL, explicit retention or an old read lease. If Spill cleanup fails, deletion
reports the failure and keeps the conversation record available for retry.
Cleanup can have removed part of the directory before reporting an error; this
is not an atomic rollback of already deleted artifacts.

This does not clear other conversations, global legacy files, independent CLI
or workflow scopes, or retained child conversations with their own transcripts.
Those continue under their respective lifecycle and retention policies.

### Read-only spill inspection in Settings

The Settings storage card displays the main spill directory from
`DefaultSpillDir()` and opens an operator-only directory/file browser. In
multi-user deployments both browsing endpoints require the administrator
grant; ordinary accounts retain only their existing session-scoped tool access.
The shared token in single-user deployments represents the operator.

`GET /api/prefs/spill/entries?path=<relative>&offset=0&limit=100` returns
`root`, `path`, `parent`, `read_only`, `exists`, `entries`, `offset`, `limit`,
`next_offset`, `complete`, `truncated`, `skipped`, and `scan_limit_reached`.
Each entry has `name`, `path`, `type` (`directory` or `file`), `size`, and
`modified_at` (UTC). Empty `path` means the configured spill root. A missing
root returns an empty complete view with `exists=false`, without creating it.
Dot directories such as `results/.metadata` remain inspectable.

Listings use native filesystem order rather than loading/sorting an unbounded
directory. `offset` counts native entries including rejected special files;
`next_offset` resumes at the next uninspected entry. Each page returns at most
200 entries, inspects at most 4096 new entries, and skips at most 100000 earlier
entries. The browser never scans beyond entry 100000: if more exist, it returns
`scan_limit_reached=true`, `truncated=true`, `complete=false`, and no next offset.
This is a live directory view; concurrent creation/deletion can change offsets.
There is no total-entry count or snapshot-completeness promise.

`GET /api/prefs/spill/file?path=<relative>&offset=0&limit=32768` returns
`root`, `path`, `name`, `read_only`, `size`, `modified_at`, `offset`, `bytes`,
`next_offset`, `complete`, `truncated`, `binary`, `encoding`, and `content`.
The byte limit is 4..65536. UTF-8 text pages stop at complete rune boundaries;
`bytes` and `next_offset` describe the bytes actually delivered. Binary or
invalid-UTF-8 chunks return `binary=true` with empty content. HTML is returned
only as a JSON string for a plain text preview, never served as an executable
document. Responses use JSON, `no-store`, and `nosniff`. Deleted paths return
404 with a refresh hint. Noncanonical/absolute paths are rejected, and pinned
root-relative handles reject symlinks, directories as files, and special files.

Inspection never invokes `ResultStore.Load`, writes data, renews activity
leases, or changes record bytes. It does not bypass session isolation for agent
tools; this is a separate administrator inspection surface.

MCP records include model text, structured JSON when supplied, and the SDK's
re-serialized native result, including resources and media. This is not a
byte-for-byte HTTP capture. Text resources and resource-link URIs are visible
to the model. Binary data stays in the saved native record, and the text reader
returns metadata for media `data` and resource `blob` fields. This does not add
MCP `resources/read`, image analysis, or automatic remote-resource fetching.

A saved record is capped at 80 MiB encoded JSON. When the combined payload cannot
be saved, the host first attempts to retain text and structured data without the
original native payload, then all text alone. A successful fallback is explicitly
incomplete; its notice and `native` directory's `/pogo_storage_fallback` identify
which original parts were not preserved. Restoring a legacy full text file does
not erase an existing payload-incomplete flag. If no useful content can be saved,
there is no result ID and the model is told persistence failed. Existing usable
text spill paths remain available when present. Missing original parts never
become a claim that full source content was recovered.

## Model-facing previews

The existing 2,000-line / 50 KiB thresholds remain the compatibility baseline.
The prepared UI/history observation keeps its existing deterministic preview of
about 12 KiB / 200 lines:

- JSON: shape, observed counts and bounded samples in a distinct JSON envelope;
  existing status/error/pagination fields take priority.
- Logs: beginning and end.
- Web pages/documents: more space for the beginning, plus a tail preview.

Counts describe the saved observation, not an entire remote dataset. Samples
are not aggregates or a substitute for the original schema. Small results stay
inline with a recovery ID. No model call is used to summarize tool output.

The request-only initial delivery is selected separately, before context-budget
allocation. In default `auto` mode, large JSON and classified logs receive a
reference-first view of at most 4096 bytes: status, storage size, bounded JSON
directory/navigation and recovery instructions, without arbitrary array samples
or log head/tail. Web pages/documents keep bounded previews. Small observations
stay inline. Original errors and incomplete storage retain explicit diagnostics.

Hosts can set `Options.ResultDelivery` to `auto`, `inline`, `preview` or
`reference`, globally or by exact tool name, and optionally choose RFC 6901
focus pointers or literal keywords. The configuration is cloned for registry
snapshots; third-party result text cannot select it. It never adds arguments to
MCP calls or changes saved bytes, UI text, source stamps or citation numbering.
All modes still obey the later model-context budget. Tightening a reference-first
view does not bring array samples back, and `tool_result` pages retain their own
authoritative body ranges. Disabling the tool-output budget also disables these
request-only initial views for the existing comparison/control contract.

`read`, `ls`, `find`, `grep`, `todo`, and `tool_result` do not create result chains.
Successful host-generated pages are not truncated a second time: their offsets
and body ranges must still describe exactly what the model received.

## Reading without repeating an action

`tool_result` is a protected native tool, including in strict approval mode. It
never edits source data or executes the original action. It accepts `result_id`
and an optional `view`; only `list` does not require an ID. Retain/release edit
local lifecycle metadata only, like other session coordination operations:

| View | Reading contract |
| --- | --- |
| `summary` (default) | Bounded overview and directories; not a claim of full coverage |
| `text` | UTF-8 byte ranges; `offset` starts at zero; `limit` defaults to 8192, maximum 32768 bytes |
| `json` | Structured JSON, or an unambiguous JSON text result; RFC 6901 `pointer` selects a value |
| `native` | Saved native protocol result; empty pointer gives a directory only; encoded media remains metadata |
| `query` | Filter/project, sort or group/aggregate a saved JSON array; never calls the original server |
| `search` | Literal saved-text search with bounded context and exact source byte ranges |
| `list` | Current-session metadata directory; optional exact `tool` or literal metadata `filter`, default 20/max 100 entries, bounded to 24 KiB |
| `retain` | Protect an existing current-session ID for `days` (default 30/max 90); does not refresh its source |
| `release` | Remove explicit retention; does not delete the result or revoke active leases |

Catalog `offset`/`next_offset` are directory scan cursors, not matching-item
counts. Each returned item includes `catalog_offset`, and budget fitting advances
only past entries actually shown. Directory changes can affect positions. Old
records are backfilled under an eight-record/80 MiB per-page budget; exhausting
that budget returns a continuation before the next unread candidate. Real
unavailable metadata is reported as skipped, never as proof that no record exists.
Catalog and retention receipts are host metadata and spend no citation numbers.

Selected arrays use item `offset` and `limit` (default 20, maximum 200).
Selected strings use UTF-8 byte ranges. `next_offset` describes the actual
returned range. A budget smaller than one UTF-8 rune may return that one complete
rune so every nonempty page can advance. Oversized array items are explicitly
marked omitted with a pointer suffix; they are never silently skipped.
Duplicate JSON keys are not projected into a misleading single field value;
the original text remains available by byte range.

The reader's JSON responses stay within 40 KiB. Original failure and partial
coverage remain visible even when the read itself succeeds. There is no
arbitrary code execution or path parameter.

The tool description directs the model to select relevant fields/ranges, use
aggregate queries for saved-array arithmetic rather than samples, and change a
request whose model-budget receipt omitted its page instead of repeating it in a
loop. This is model guidance, not a guarantee of optimal call selection. A read
canceled during local decoding/pagination/encoding is not delivered as a
successful complete page; this does not make the JSON decoder interruptible.

## Sources of recovered results

The citation registry associates host-created result IDs with their original
artifact stamps. Successful `text`/`json` body reads with a unique original source
reuse its label, locator, hash and observation time. Storage access does not
refresh the remote webpage or file. A JSON omission/directory receipt does not
inherit a webpage stamp merely because the original ID came from a page.

`query`, `summary`, and `native` views retain distinct local-result stamps.
Queries record their validated arguments and, when known, the original artifact
as parent metadata under `saved_result`; a computed value is not attributed as a
quotation from the original webpage. Existing UI body/history text remains
unchanged. Source-less process metadata, such as bookmarks and todo state,
remains source-less when read back.

Content-addressed IDs can be shared by identical output from different artifacts.
If locators or observation hashes disagree, the host marks origin ambiguity and
does not choose one webpage arbitrarily. Resume/rewind reconstruct associations
from reachable host source metadata, including IDs made by budget snapshots and
retained reads. A recovered raw read can keep its origin after the original call
has been compacted away. If all supporting stamps were removed, the saved content
can still be read in the same store, but its original locator is unknown; the
host does not infer it from result text or summary prose. Imported transcripts
discard local saved-result links and nested parent grants.

## History, compatibility and boundaries

The transcript persists `ResultID` as local metadata. Context editing retains a
trusted recovery ID rather than telling the model to repeat the original action.
Compaction is instructed to retain IDs still relevant to the task; this is a
summary instruction, not a guarantee that every ID survives an LLM summary.

Legacy file reading remains supported. Single-user Web and CLI resume may grant
the exact regular legacy spill files already referenced in stored tool details;
they do not grant the entire shared directory. Owner-isolated Web sessions do
not restore such grants, because old shared files lack owner provenance.
Imported transcripts preserve displayed conversation data but do not import
result IDs or legacy file grants. Copying/importing a conversation does not
transfer its stored observation files.

## Adaptive model-text budgets

The agent allocates a text budget after a complete tool batch settles, in call
order. Parallel completion order cannot decide which result receives space.
Defaults use the configured model window:

- Per result: `min(8192, context_window / 32)` estimated tokens.
- Per batch: `min(24576, context_window / 10)` estimated tokens.
- Unknown window: 8192 per result and 24576 per batch, rather than zero space.
- Live batches also respect estimated remaining space after the current prompt,
  configured maximum output, and `max(1024, context_window / 20)` safety reserve.
  An unspecified output cap reserves 16384, matching the client default.

`agent.ToolOutputBudgetConfig` supports explicit per-result/batch overrides and
`Disabled`. Web, CLI, workflow and model-switch paths pass the selected model's
window/output cap. Limits are local `ceil(UTF-8 text bytes / 4)` estimates,
not exact tokenizer counts. Images keep their existing delivery path; this is
not an image token limiter. Provider input usage calibrates the existing prompt
baseline, while changes are estimated on the same model-visible representation.

Small results release unused allocations. Errors receive priority up to 512
estimated tokens when space allows; remaining capacity is shared fairly. Each
nonempty result retains a planning allowance of up to 128 estimated tokens for
identity/status. Mandatory identity, citations, unavailable storage and expired
references can exceed a target. The host never drops a call/result pair or
destroys the only copy of a page merely to satisfy an estimated budget.

A successful `read` whose only file is a registered Skill's `SKILL.md` is the
one exception to the per-result and per-batch limits. The model loads it to
follow it word for word, and a preview would be a partial rule set it cannot
tell from the real one: the built-in `browser-use` document (33 KB, about 8,300
estimated tokens) exceeds the per-result limit, and as a preview it lost 14 of its
166 substantive lines at a 1M window and 88 at 128K. Such a result is
allocated its full cost first; the read tool's own 2000-line/50 KB page still
bounds it, and only the measured remaining headroom can cut it short, never below
its identity allowance. The other results share the batch target as before,
within what is left. Attribution is the registry's trusted Skill attribution
(`SkillDocumentReadAlone`, the same evidence compaction uses to restore a loaded
Skill): a same-named copy, a multi-file read or a failed read is budgeted as an
ordinary page. Documents read through `skill_read` keep the ordinary limits.

Budgeting changes a copy sent to the model. UI cards, transcript bodies and their
line/body offsets stay whole. An ID-less file/search page is saved before its
view is reduced. An existing ID reads the saved original; it is never replaced
by a preview falsely marked complete. A `tool_result` read is not saved again:
if its page cannot fit, the view explicitly says `page_displayed=false` and
points back to the same immutable query with smaller limits.

Cached views only shrink within the current history. Resuming or compacting a
conversation reconstructs that cache for the new context. Context editing and
compaction use the same bounded views, retaining recovery IDs. The optional
`tool_output_budget` field on `turn_start` reports estimates, reduced results,
freed text tokens, targets, minimum required allowances and budget overruns.
The last live-batch target remains visible even when it is zero; mandatory
identity-floor excess, actual batch excess and unrecoverable per-result excess
are reported separately. Historical reconstruction has no live-batch headroom
measurement and marks that distinction explicitly.
These are request-view diagnostics; they do not assert a provider-token hard cap
or overwrite the original transcript.

The protected reader has a fixed declaration cost as well. The current measured
browser-session schema is 25145 wire bytes across the same 13 tools; the
standalone `tool_result` declaration is 6480 bytes. Catalog, retention, sorting
and grouping reuse this reader, with duplicate prose shortened to stay within
the unchanged 25200-byte `TestToolSchemasStaySmall` ceiling. Budget savings below
concern tool-result text, not declaration cost or provider billing.

Budget-generated snapshots preserve host UI recovery metadata separately from
the original native protocol. Known MCP image/audio/resource binary fields in
that metadata are replaced with omission metadata before nesting beneath
`/details`; paging that nested directory cannot restore those omitted bytes.

## Structured filtering and aggregation

`tool_result` with `view=query` selects an array using `pointer`. It supports:

- `where`: at most 16 ordered AND conditions with `eq`, `ne`, `lt`, `lte`, `gt`,
  `gte`, `contains`, or `exists`. Missing comparison fields and type errors are
  explicit errors; use an `exists` condition before comparing an optional field.
- `select`: at most 32 RFC 6901 field pointers. Projections distinguish a missing
  field from an explicit JSON null.
- `aggregate`: `count`, `sum`, `min`, `max`, or `avg` over all matching rows of the
  saved array, independently of row pagination. Decimal JSON numbers use bounded
  exact rational arithmetic; values are strings in integer or numerator/denominator
  form. Missing/null/non-numeric counts are separate, and no valid numbers yields
  null instead of an assumed zero.
- `sort`: up to four `{pointer,direction}` keys (`asc`/`desc`), applied before
  pagination. Scalars must have a consistent non-null JSON type per key;
  numbers compare exactly. Null then missing sort last in either direction.
  Equal keys retain original array order, and original indices remain truthful.
- `group_by`: up to four scalar field pointers, with a required aggregate.
  Missing/null remain distinct and equivalent JSON numbers share a group. The
  entire selected saved array is scanned before groups are ordered and paged.
  At most 1000 groups and 8192 encoded bytes per combined key are allowed;
  exceeding either fails without returning partial statistics.

Grouped queries forbid `select` and `include_rows=true`. Their `offset`/`limit`
count groups (default 20/max 200), and each item contains `group_index`, explicit
key presence/values, count and aggregate. Sort pointers then address group
objects, such as `/count` or `/aggregate/value` (exact rational comparison).
`groups_included`, `total_groups` and the range unit distinguish these derived
statistics from original source rows. Group values never acquire raw array-row
quote coverage. Budget continuation preserves grouping, aggregate and sorting;
an undisplayed group cannot be recovered as an invented original JSON row.

Aggregation defaults to `include_rows=false`, returning just statistics and the
aggregate. Set `include_rows=true` to also inspect matching rows. A query without
an aggregate includes rows by default. `offset`, `limit`, and `select` require
rows to be included; they cannot be silently ignored in an aggregate-only query.
Row pagination uses matching-row positions and retains each original array index.
The default is 20 rows, maximum 200. Oversized rows receive explicit omission
receipts with pointers for subsequent reads.

Queries reject arrays larger than 100000 rows before scanning, rather than
presenting partial aggregates as complete. They check cancellation and bound
conditions, pointer depth, and numeric precision/exponents. Query arguments use
strict parsing: unsupported expressions or join/code fields, duplicate keys,
missing required fields and invalid nulls are rejected. Existing read views keep
their compatibility behavior.

`scan_complete` means the selected saved array was scanned, not that the remote
source was complete or current. Original tool failures and incomplete observations
remain visible. No JavaScript, SQL, shell, network access or arbitrary user code
is introduced. General programmatic tool orchestration is a separate capability.

## Delivery, catalog and ordering validation (2026-10-21)

The bounded follow-up change passed the tools, agent and evidencebounds package
regressions, targeted race checks, metadata/source-reopen checks and the 19-record
legacy-catalog continuation case. Linux ARM64 and Windows tool-test artifacts
cross-compiled; these are compile checks, not platform runtime acceptance.
The reader stayed within the existing 25200-byte schema ceiling (25145 measured).

The same three single-attempt real-model recovery tasks passed their core and
independent actual-delivery scope reviews: GLM compact/save/reopen, Kimi
compact/save/reopen, and Kimi-to-GLM switching. Frozen sources/config/binary
remained unchanged during the group; there were no extra grader requests.
Original exact-wording verdicts remain unmodified, including GLM/switch false
values whose bounded scope language passed independent semantic review. This
does not rerun or reclassify the earlier broader v9 evaluator calibration.
Sorting/grouping additionally has deterministic native and agent delivery tests;
the three real-model tasks concern recovery, not a new grouping benchmark.
No Docker deployment was performed for this follow-up change.

### Bounded real-model grouping follow-up

The separate `grouped-records` task ran once with GLM and once with Kimi over
the same 10,000-row observation. Both correctly grouped all 3333 active records,
ranked region sums and projected the top five sorted records. Each made one
original MCP call and two native saved-result queries, with no file-tool calls.
Actual successful model requests contained the required computed groups and
ranked rows; initial delivery contained only the recovery reference and metadata.
All 10,000 saved rows were independently recovered unchanged after each task.

Original strict task verdicts were one pass and one failure: GLM additionally
cited the verified original provenance alongside both computed-result sources.
The task did not forbid that citation. The evaluator now allows verified
original provenance as an additional source while still requiring both actual
group-statistic and sorted-row sources. Original-only, missing either derived
source, unknown labels and unverified provenance remain failures. Added negative
checks and a hash-verified offline replay pass for both original request ledgers;
no additional model requests were made and original reports were not rewritten.

The shared workspace changed during these trials, so the original whole-source
freeze gate remains failed. The tested binary, provider configuration, task,
fixture, system prompt and tool declarations stayed identical across the two
tasks. This supports the fixed compiled artifact's scoped functionality, not a
release acceptance for the current combined workspace or a stable cost/latency
benchmark. No Docker deployment was performed.

## Automated validation (2026-10-21)

Added 63 top-level regression tests, with additional table/subtest cases, for
storage integrity and scope, recovery, UTF-8/JSON pagination, native media,
strict queries and exact aggregates, cancellation, budgets, source associations
and imported metadata. These use temporary stores, scripted model clients and
local HTTP fixtures. The Information acceptance also executes real CLI child
processes in foreground, background and retained modes; it checks that the
observed fact and asset SHA actually reach the next model request rather than
trusting the scripted final answer.

The test run found and fixed these production regressions:

- A fitting stamped result was being charged as separately rounded header/body
  estimates, causing a one-token shortage that replaced small JSON with samples.
  Keep the complete result when it already fits. Regression cases cover all
  16 header/body byte alignments with budgeting enabled/disabled and history
  reconstruction.
- Empty successful output was turned into synthetic citation evidence. Preserve
  the empty output while keeping real payload/legacy full-text recovery paths.
- A provider-measured token count was discarded when the local estimate was
  zero. Zero is a valid local baseline, not a missing measurement.
- Cancellation during the final storage read could return a successful load.
  Recheck cancellation after reading and decoding.
- Direct MCP callers/context sentinels lost their original error identity.
  Keep those contracts while bounding managed error markers and preserving the
  complete failure payload in storage.

Passing package regressions:

```sh
go test . ./agent ./tools ./wire ./llm ./session ./mcpobserve -count=1
TMPDIR=/private/tmp go test ./web -count=1
```

The canonical macOS temporary directory is required by the existing Office
managed-path checks; `/var` is a symlink to `/private/var` on the test host.
The initially failing Office cases pass under that canonical temporary directory
without changing Office production code. New result/reader/query/budget/source
tests also pass targeted `-race` checks.

A scripted loop comparison reduces the result-text estimate from 7560 to 512
tokens (93.2%). Both runs execute the action once and call the scripted model
twice; UI/history text and the immutable ID are identical and the whole original
is recoverable. This fixture does not establish a production-wide saving rate.

Production packages compile. A blanket `go test ./... -run '^$'` also discovers
five archived `browser-runtime/eval/runs/.../protocol-source` directories whose
copied `main.go` files lack supporting definitions; those unrelated snapshots do
not compile. Excluding those archived directories passes the compile scan.

Automated backend regression alone does not establish real-model task quality.
The controlled API acceptance below was run subsequently. Docker runtime
validation and deployment were not performed.

## Controlled real-model acceptance (2026-10-21)

`cmd/tool-result-eval` runs the configured default model against a local SDK MCP
HTTP server returning one fixed 10000-row observation. The fixture offers no
filtering/aggregation API. The agent has an isolated workspace and result store;
fixture generation, the oracle, provider configuration and reports are outside
its readable roots. Provider credentials and reasoning text are not logged.
Both modes retain first-stage storage/previews and the same reader schemas;
`off` disables only the second-stage model-view budget.

The deployed configuration selected `glm-5.3`. The task filters East/active/risk
at least 60, then asks for count, integer-cent sum/min/max and the first five
matching records in original order. An independent Python oracle and host
fixture calculation agree: count 330, sum 16058400, min 90, max 99746; first IDs
186, 198, 210, 222, 234. The model was never given that oracle or generation
formula.

| Formal citation rules, one trial each | API input tokens | API output tokens | Model requests | Result reads | Outcome |
| --- | ---: | ---: | ---: | ---: | --- |
| Default automatic budget | 16201 | 1331 | 3 | 4 | Correct data, strict output, citations and query-delivery evidence |
| Extra budget disabled | 21890 | 1317 | 4 | 5 | Correct JSON values and query evidence; extra prose after the JSON failed strict output format |
| Stress: 384 per result, 2048 per batch | 62484 | 3234 | 9 | 11 | Incomplete; stopped at the run token budget |

Each trial fetched the original MCP observation once. Host recovery read all
10000 rows in 50 pages and matched the dataset SHA-256 in every trial. Formal
default auto had zero reduced results: the existing first-stage preview was only
about 2 KiB, already below its default cap. Therefore the on/off token/latency
difference is not evidence of savings from automatic budgeting. These are
single task samples with differing model paths and cache behavior, not stable
performance estimates.

Two earlier trials using abbreviated citation guidance also had correct values
and recovery, but used sequential display numbers instead of the strict `[1]`
format. Those records were preserved. Formal trials append the actual
`agent.CitationSection()` and record the full prompt hash. Original strict
failures remain failures; a separate audit distinguishes correct JSON values
from an invalid final response format instead of calling format failures
arithmetic mistakes.

The 384-token stress trial exposed a real recovery failure: the model repeatedly
requested the same five-row page after its model view omitted the rows, then
tried larger text/JSON ranges and invalid paging parameters. Sum/min/max were
delivered, but the first five rows never reached the model, and no final answer
was produced. Total reported input plus output was 65718 tokens; the 60000 run
cap is checked between requests and can be exceeded by the last request. Smaller
individual views did not reduce total task cost in this trial.

This failure motivated the budget-aware continuation described below. The
baseline trials above predate that change and remain unmodified. The default
production budget remains unchanged.

The evaluator records original output hashes separately from model-request body
hashes, so a result shown only in a UI card does not prove model delivery. Its
evidence checks are conservative for rewritten budget views. Citation labels and
format are checked separately; this is not a general semantic citation verifier.
No Docker deployment or long-conversation production acceptance is implied.

## Budget-aware continuation and recovery guards

The host adapts successful native `tool_result` pages before returning an
omission receipt. It compacts repeated management explanations, then returns
the largest complete prefix that fits the allocated model view. Query offsets
count matching rows; JSON/native array offsets count selected array items.
`shown_items`, `next_offset` and `complete` describe content actually present
in that model request, independently of the original UI/history page. A row
omission placeholder cannot advance the model's continuation past unread data.

Text and selected JSON/native strings retain a UTF-8-safe prefix with actual
byte continuation offsets. Encoded JSON size, including escaping, is measured
before delivery. Exact aggregates remain atomic and concern the whole selected
saved array, independently of row pagination. Delivering an aggregate alone
does not fulfill a request that also asked for rows. Original failure and
stored-text completeness flags remain visible; none of these views establish
remote/source completeness.

If no requested content fits, the host returns an explicit omission with an
available narrower recovery selection. It does not create another saved
result or replay the original MCP/action. Run-local duplicate protection
normalizes host defaults and argument ordering, and suppresses the same
undeliverable selection at the same or lower available budget. New offsets,
changed selections and greater available capacity can proceed. It applies
only to the protected native reader after approval argument rewrites. A
suppressed read is marked `request_executed=false`, has no new citation source,
and does not claim to have delivered a page.

Delivery accounting follows the actual selected model view, including cache
fallbacks. Historical reconstruction does not turn earlier omissions into new
execution guards. A new run, history/model/budget replacement, or successful
compaction resets guard state; failed compaction commits preserve current state.

Regression coverage includes real Agent/Registry continuation at 384 tokens,
first-five reconstruction without replay, escaped and Chinese UTF-8 strings,
large-row omissions, business fields named `omitted_value`, semantic duplicate
requests, approval rewrites, cache fallbacks, and compaction success/failure.
The evaluator parses actual model-request pages, rather than treating an
original UI result as delivery. It rejects false row counts, skipped offsets,
fake completion and omitted rows while combining valid partial pages.

### Recovery retest (2026-10-21)

The same `glm-5.3` fixture, task, system prompt, schema and production defaults
were retained. Two new 384/2048 stress trials both passed strict JSON, values,
coverage, citations, actual-query delivery and complete host recovery. In both,
the first five records actually reached the model as a three-row prefix with
`next_offset=3`, followed by two rows at offset 3. Each fetched the MCP source
once and recovered all 10000 saved rows with the original SHA-256.

| Trial | API input tokens | API output tokens | Model requests | Result reads | Outcome |
| --- | ---: | ---: | ---: | ---: | --- |
| Original 384-token stress | 62484 | 3234 | 9 | 11 | No final answer; run token budget exhausted |
| Recovery stress 1 | 22985 | 2022 | 4 | 5 | All acceptance checks passed |
| Recovery stress 2 | 22985 | 1810 | 4 | 5 | All acceptance checks passed |
| Default-budget regression | 22492 | 1519 | 4 | 8 | Correct data/delivery/recovery; citation array format failed |

The default-budget trial put four valid citation anchors into one `sources`
array element, instead of one anchor per element. Its strict citation check
remains failed; this is not an arithmetic or recovery failure. Its first four
reader calls omitted `view=query` and correctly failed before being retried
with valid arguments. Automatic budgeting never activated in that trial, so
those extra calls are not evidence of a new paging regression. No prompts or
acceptance rules were changed to turn this failure into a pass.

The eight backend packages (`.`, `agent`, `tools`, `cmd/tool-result-eval`, `llm`,
`wire`, `session`, `mcpobserve`) passed complete regression. Targeted result,
budget, recovery and actual-model-page tests passed `-race` across three
packages. These are limited acceptance samples, not stable production savings
estimates or general semantic citation verification. No Docker deployment was
performed.

## Raw-log acceptance (2026-10-21)

`cmd/tool-result-eval --scenario logs` adds a separate synthetic raw-log task.
It preserves the records scenario's original defaults, prompts and fixture.
The local real SDK MCP server returns 2881729 UTF-8 text bytes (20002 lines)
once, without a structured array or server-side query. The model must identify
a recovered retry, the root-cause span, parent propagation and a delayed final
audit record, quoting four exact source lines in original order. Three required
lines are in the middle and absent from the first-stage head/tail preview.

The first paired trial supplies an approximate incident time and a trace ID.
Both modes found all four original lines, distinguished the recovered timeout
from the actual version-mismatch root cause, and distinguished audit time from
business failure time. Actual model-request range receipts and an independent
88-page full-text recovery were checked separately. Both fetched the MCP source
once, used no file tools and recovered the same original text SHA-256.

| Budget mode | API input tokens | API output tokens | Model requests | Result reads | Facts and actual evidence | Strict overall outcome |
| --- | ---: | ---: | ---: | ---: | --- | --- |
| Automatic | 72470 | 5614 | 5 | 3 | Correct | Failed: expanded the fixed coverage field |
| Extra budget disabled | 83114 | 7817 | 5 | 4 | Correct | Passed |

The automatic trial's expanded coverage still explicitly limits its conclusion
to the saved observation; the original fixed-field check remains failed. Its
answer, source identity and original quoted evidence are correct. It reduced
one model view; differing request choices, reasoning and cache behavior prevent
interpreting these single-trial costs as a stable savings rate. The 60000-token
run budget is checked between requests, so the last request can exceed it.

The working directory contains no fixture code, oracle, provider configuration
or report. Protected file tools still exist and can access session spill roots;
the harness does not claim a hard filesystem denial. Any file-tool call fails
acceptance, and these trials made none. Evidence validation rejects host/UI
results not actually sent to the model, request-pattern metadata, fabricated
range headers, source mismatches and missing bytes within a required quote.
Adjacent genuine ranges may jointly deliver one complete UTF-8 line.

These trials validate a synthetic, roughly time-ordered log with a useful time
hint, rather than arbitrary unsorted logs. They do not establish long-session
recovery or general semantic citation verification. No Docker deployment was
performed.

### Trace-only boundary and saved-text search

A separate task variant, `--scenario logs --log-trace-only`, removes only the
user-supplied incident-time sentence. The system prompt, source bytes, oracle
and remaining task are unchanged. The log's tail still contains the original
business-event time, so this does not simulate a complete absence of time clues.
Its pre-search trial made one incorrect JSON query and guessed text offsets.
All required evidence eventually reached the model, but it continued reading
and stopped at the run token budget without a final answer: 59293 input and
1648 output tokens, five model requests and four result reads. Full source
recovery still passed. This is an inefficient task-completion failure, not lost
storage or proof that the root cause was impossible to locate.

The same protected `tool_result` reader now supports `view=search`. It searches
the decoded saved `Text` with a case-sensitive literal `pattern` (1..1024 UTF-8
bytes). `context_lines` defaults to 1 and is bounded to 0..5. `offset` counts
non-overlapping matches, while `limit` defaults to 20 and is bounded to 200.
Each item separately names its match and context byte ranges, one-based line
number, actual text and whether context was shortened. Snippets are limited to
4096 UTF-8 bytes and retain the complete literal. No file path, regular
expression, native-media decoding or original-tool request is involved.

The scan counts all matches while retaining only the requested bounded page.
Cancellation is checked between bounded scan windows. `scan_complete` and
`total_matches` concern the saved text; page `complete` and `next_offset`
concern matching items actually delivered. Budget adaptation retains whole
matching items and reports the true continuation. A hit that cannot fit gets
a narrower-context or exact-byte recovery request, never an offset that skips
unread data. Original error/partial flags and limited source coverage remain
explicit. Searches are local derived citations with the original parent
artifact, not refreshed source observations.

The first-stage plain-text preview now points to literal search as well as byte
ranges. This retrieval guidance and the new reader declaration are both part
of the change; the original saved text remains identical.

The always-registered reader's added declaration costs 501 wire bytes in the
measured thirteen-tool session: 24994 total versus 24493 before search. The
schema ceiling is 25200, retaining 206 bytes of headroom. Performance comparisons
must account for this schema change rather than attributing keyword retrieval
gains solely to the optional second-stage model budget.

The search retest keeps the trace-only task, system prompt, fixture and oracle
unchanged and declares the reader schema change above. Both automatic and
budget-disabled modes passed the strict final JSON, exact four quotes, root
cause/span/time facts, coverage field, citations, actual-delivery checks and
full source recovery. Each executed one original MCP call and one saved-result
search, with no file-tool calls or tool errors. The search delivered five
matching contexts whose original-byte union was 2446 bytes; this excludes
management text, metadata and the separately delivered initial preview.

| Same trace-only task | API input tokens | API output tokens | Model requests | Result reads | Outcome |
| --- | ---: | ---: | ---: | ---: | --- |
| Before search, automatic budget | 59293 | 1648 | 5 | 4 | Budget exhausted; no final answer |
| With search, automatic budget | 33946 | 2514 | 4 | 1 | Passed |
| With search, extra budget disabled | 33946 | 3121 | 4 | 1 | Passed |

Neither search retest activated second-stage budget reduction. The improvement
is bounded keyword retrieval replacing noisy byte-range probing, not a measured
saving from an inactive budget layer. These are single trials in each mode;
the different cache and reasoning observations do not establish stable latency
or production-wide token savings. Long-conversation recovery remains untested
by this log acceptance.

The eight backend package regressions and targeted source/result/search/model-
delivery race checks passed. The first broad eval race check hit an existing
20-second timeout while recovering the SDK records fixture's 50 pages. That
test-only deadline is now 60 seconds, with all recovery/identity assertions
unchanged; the previously failing check and new log/search checks pass under
race instrumentation. Production run/request/recovery limits were not changed.
No Docker deployment was performed.
