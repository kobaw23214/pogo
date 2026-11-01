# MCP connection export

The MCP settings provide two exports:

- **This Pogo instance** creates connection settings for an agent that will call this instance's MCP endpoint.
- **Configured connections** transfers selected upstream MCP connections to another agent.

Choose Pogo, Claude Code, Cursor, or VS Code, review the generated JSON and compatibility notices, then copy or download it. Export uses saved connection settings and the effective server state. Editing an unsaved settings form does not change the export.

## Credentials and addresses

Exports use environment-variable references for credentials. The receiving agent must have the listed variables set in its own process environment. Server credentials are not resolved or embedded in the exported file. Literal environment values and sensitive headers in upstream configurations become references; the preview lists the values that must be configured at the destination without showing the original secrets.

For an instance export, enter the full MCP URL that the receiving agent can reach. Check the host, published port, HTTPS scheme, and any reverse-proxy prefix. A loopback address reaches the receiving machine itself; use an appropriate network address when the agents run on different machines. The export does not enable the MCP server or change its tool allowlist or approval policy.

Local stdio connections also require the command, files, and any working directory to exist on the receiving machine. Exporting their configuration does not install those dependencies.

## Target formats

| Target | Root key | Credential reference |
| --- | --- | --- |
| Pogo | `servers` | Native `bearer_token_env`; `${VAR}` in headers/environment |
| Claude Code | `mcpServers` | `${VAR}` |
| Cursor | `mcpServers` | `${env:VAR}` |
| VS Code | `servers` | `${env:VAR}` |

HTTP and legacy SSE connections retain their transport. Pogo's local trust, filtering, timeout, and enablement settings are not universally portable. Read the compatibility notices for the selected target before using the exported file. A receiving agent's configuration format does not replace authorization enforced by the MCP server.

Pogo can import its native `servers` format or supported `mcpServers` entries through its existing JSON import dialog. MCP tool schemas are discovered from the endpoint after connecting; an export contains connection settings, not a static tool catalog or the source instance's server configuration.

## Verification

After importing, configure the required environment variables, use the receiving agent's connection test, and confirm that it can list tools and perform an appropriate read-only call. A successful JSON parse alone does not verify the endpoint, authentication, or permissions.

The focused frontend checks are the `mcpExport`, `mcpImport`, and `mcpServe` Vitest suites under `web/ui/src/components`. The Go MCP client and server tests cover the loopback transport and authentication behavior. The existing scripts under `scripts/mcp-smoke` exercise the server with independent MCP SDKs.
