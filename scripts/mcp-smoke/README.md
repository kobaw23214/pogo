# mcp-smoke — cross-SDK smoke tests for pogo's MCP server

The Go test suite (`web/mcp_test.go`) and the probe (`cmd/mcp-probe`) exercise
the wire with the same SDK on both ends. These two scripts do it with the
official TypeScript and Python SDKs — the interop proof that a protocol layer
actually owes.

## Run

1. Start a server with MCP on (any config; a dead provider is fine, the smoke
   asserts the wire, not the answer):

   ```sh
   POGO_MCP=1 POGO_WEB_TOKEN=t pogo -web -listen 127.0.0.1:7777
   ```

2. TypeScript SDK:

   ```sh
   cd scripts/mcp-smoke
   npm install          # pulls @modelcontextprotocol/sdk (package.json)
   node smoke.mjs http://127.0.0.1:7777 t
   ```

3. Python SDK:

   ```sh
   cd scripts/mcp-smoke
   uv venv && uv pip install mcp     # or: python -m venv .venv && .venv/Scripts/pip install mcp
   uv run python smoke.py http://127.0.0.1:7777 t
   ```

Each script ends with `TS-SMOKE-PASS` / `PY-SMOKE-PASS` and a non-zero exit on
any failed assertion.

## Conformance

The official suite hammers the endpoint itself (verified green 2026-08: every
scenario applicable to a tools-only agent server passes; the prompts/resources/
elicitation/reference-fixture scenarios are baselined as expected failures):

```sh
npx @modelcontextprotocol/conformance server \
  --url "http://127.0.0.1:7777/mcp?token=t" --suite active \
  --expected-failures scripts/mcp-smoke/conformance-baseline.yml
```

The token rides the `?token=` query (the suite has no header flag); the exit
code is 0 when the only failures are baselined, 1 on any new regression or a
stale baseline entry.

