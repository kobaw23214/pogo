# Browser-only evaluation agent

This command runs the existing `agent.Agent`, `tools.Browser`, and configured
`llm.Client`. It registers only the browser tool and uses no alternate LLM loop.
It does not discover repository instructions, user/project skills, shell tools,
or MCP tools. The independent `general-web` profile loads the real built-in
`browser-use` skill body; the default `browser` system prompt remains identical
to the first evaluation baseline. Each final record identifies the actual prompt
by SHA-256. Keep separate executable names to retain historical baselines.

Build from `pogo/`:

```sh
go build -o /tmp/pogo-browser-eval-agent-capabilities ./cmd/browser-eval-agent
```

The evaluator creates an isolated worker session, navigates it once, then starts
this command with one JSON object on stdin and closes stdin. The command performs
an idempotent session PUT; it does **not** repeat the starting navigation. The
agent observes the already-open page using its browser tool.

```sh
/tmp/pogo-browser-eval-agent-capabilities --model MODEL --providers /path/to/providers.json < request.json
```

The configured model credential must already be available through the provider's
`key_env`. `POGO_BROWSER_EVAL_TOKEN` supplies the worker bearer token. `--providers`
selects the same configuration as `POGO_CONFIG`; otherwise the normal project
configuration lookup applies. The explicit `--model` takes precedence over the
stdin model. There is no implicit default model.

```json
{
  "prompt": "Complete the assigned fixture task.",
  "start_url": "http://127.0.0.1:PORT/scenario/ID",
  "worker_url": "http://127.0.0.1:WORKER_PORT",
  "session_id": "eval-case",
  "model": "MODEL",
  "max_turns": 40,
  "token_budget": 50000,
  "time_budget_ms": 120000
}
```

Omitted or zero budgets use those defaults. Bounds are 1–200 turns, 1–1,000,000
reported input-plus-output tokens, and 1–600,000 milliseconds. Optional
`cost_budget` requires a declared model price. Token and cost limits use actual
provider reports, so the final model call can exceed the allowance; its tool
calls are not executed after that limit is reached. Wall-clock expiry interrupts
both model and browser calls. The outer runner should keep its process watchdog:
cleanup may need up to 15 additional seconds for a cancellation acknowledgement.

Stdout contains only JSONL records:

- `tool_start`: tool call `id` and parsed `args`.
- `tool_end`: `id`, `ok`, `output`, `duration_ms`, and an error marker on failure.
- `message`: settled assistant text and that response's usage; never reasoning.
- `end`: `answer`, `end_reason`, total `usage`, `usage_measured`, model, provider,
  `sees_images`, `context_window`, `max_tokens`, protocol `compatibility`, profile,
  system prompt hash, `tool_schema_sha256`, `schema_bytes`, `cost`, and optional
  `cost_unit`. `general-web` also records `shared_skill_body_sha256`. The tool hash and byte count cover the actual registry rendered
  by `llm.ToolsWireJSON`, including descriptions. Provider URLs, headers,
  and extra request bodies are never included.

Usage has `input`, `output`, `cache_read`, and `reasoning` counts. Cache and
reasoning are subsets, not extra budget consumption. `usage_measured:false`
means no positive provider usage was reported; the zero counters are not a claim
of free inference. `cost:null` means unknown, including absent usage. A declared
price uses `cost_unit:"configured"`; the project price contract does not name a
currency. Browser success is not task success; the external fixture judge is the
authority.

Images are validated, held only in process memory, and returned by reference.
The adapter explicitly declares the native backend. Native tool schemas exclude
`eval` and nested batches. With `sees_images:true`, the tool uses Direct image
mode and `Agent.ImageResolver` supplies real pixels. Text-only models use None:
`screenshot` and coordinate clicks are hidden and rejected before reaching the
worker. DOM actions remain available, and the outer evaluator can still collect
audit frames independently.
Screenshot payloads and reasoning are excluded from logs. Each browser execution
has a random `op_id`. Transport/protocol failures stop the run without replaying
an action, then request bounded cancellation. No context is deleted: the runner
retains it for final-state judging and screenshots, and owns cleanup. The command
does not stop the worker service.

Exit codes: 0 for completed agent runs, 1 for interrupted/failed runs, 2 for
invalid input or configuration. No task-success claim is inferred from exit 0.
Tests use fake models and local HTTP fixtures; they do not invoke a paid model.

### General web profile

Use `--profile general-web` to evaluate the shared browser instructions with a
starting URL and a task goal. Its profile ID is `pogo-agent-general-web-v1`.
The runner supplies session transport and budgets separately; the task prompt
does not need selectors, element refs, route maps, API paths, fixture IDs, or an
expected answer. The agent observes the already-open page, operates the browser,
and verifies the requested result. Final answers use concise natural language;
this profile has `auth:false` and never adds `outcome` or `outcome_error` through
the authentication parser. JSONL remains the process transport format.

The profile uses `skills.MaterializeEmbedded` to read the production `go:embed`
copy of `skills/builtin/browser-use/SKILL.md`, then `skills.ParseFrontmatter` to
obtain its body. It does not copy the instructions into a second constant, read
private configuration to find a skill, or depend on the repository cwd. The
temporary materialization directory is removed after loading. Rebuild the
executable to evaluate an edited shared skill; disk overrides used by a normal
Pogo deployment are intentionally outside this isolated evaluation profile.

The system prompt contains a short evaluation isolation preface followed by the
actual shared skill body. The preface declares that the session is already armed,
the CLI has only the browser tool and no host activation card, and execution must
stay inside the task and runtime network boundary. It excludes direct HTTP,
local files, evaluation internals, credential export, and alternate execution
channels. Website/API authorization and human login rules remain in the shared
skill. Page operations and canonical `devtools` calls share the same Chromium
session; no separate credential-copying HTTP helper is available. Network reads
inspect captured browser traffic rather than silently sending another request.

`system_prompt_sha256` hashes the entire UTF-8 prompt, including the preface.
`shared_skill_body_sha256` hashes only the exact parsed body, including its
trailing newline. These identify instructions rather than binary builds or task
success. Historical profiles omit the shared-skill field. The native observation
is page text plus interactive-element rows; the profile does not assume a
structured table or heading tree, and the actual browser schema determines
available actions, tabs, and image capabilities.

```sh
go build -o /tmp/pogo-browser-eval-agent-general-web ./cmd/browser-eval-agent
/tmp/pogo-browser-eval-agent-general-web --profile general-web --model MODEL --providers /path/to/providers.json < request.json
```

### Retired authentication profiles

`--profile auth` and `--profile auth-auto` now return a structured
`end_reason: "retired_profile"` before reading a request, resolving credentials,
contacting a worker or starting a model. Programmatic registry and runner entry
points reject them too. No adapter translates their removed `auth_status` or
`api_read` calls into CDP traffic.

Use `general-web` or `browser-use-v2` for new capability evaluations. Their
browser tool declarations come directly from the current native implementation.
The default `browser` retains its original DOM-task prompt and a subset of the
same native actions; its live schema no longer advertises retired authentication
helpers. Schema hashes in new reports identify that change, so do not assume
new and historical scores have identical declarations.

Authentication prompts, strict JSON outcome parsers, fixture judges and archived
reports remain available for offline interpretation. Historical `completed`,
`needs_human` and `blocked` outcomes are model declarations; they do not override
the independently recorded task verdict. Existing reports are never rewritten.

```sh
go test -race ./cmd/browser-eval-agent
go vet ./cmd/browser-eval-agent
```

The three historical prompt hashes are frozen in CLI tests:

| Profile | `system_prompt_sha256` |
|---|---|
| `browser` | `9abcefdd1e931ba0b19286c77b032587d5a02f8122bdbc5bfe7ecbd436a3ca95` |
| `auth` | `f40aa9f8fe8569387a890410536ce86287258d27d7fe0550e3b61cba10229a85` |
| `auth-auto` | `c1b9be86f177848141aff8d5adf40fa68d26a7c9c052d843a4059969eb8682cc` |
