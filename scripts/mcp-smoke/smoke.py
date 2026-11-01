"""Cross-SDK smoke: drive pogo's MCP server with the OFFICIAL Python SDK.

Prereqs:  uv venv && uv pip install mcp   (or: python -m venv .venv && pip install mcp)
Run:      POGO_CONFIG=... POGO_MCP=1 POGO_WEB_TOKEN=t pogo -web -listen 127.0.0.1:7777
          python smoke.py [baseUrl] [token]

Same wire walk as smoke.mjs: initialize -> tools/list -> unknown-task error
semantics -> submit -> status -> client_ref dedup -> cancel. The point is
interop with a non-Go client, not the agent's answer.
"""
import asyncio
import sys

import httpx
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:7777"
TOKEN = sys.argv[2] if len(sys.argv) > 2 else "t"


async def main() -> None:
    http = httpx.AsyncClient(headers={"Authorization": f"Bearer {TOKEN}"})
    async with streamable_http_client(
        f"{BASE}/mcp", http_client=http
    ) as (read, write):
        async with ClientSession(read, write) as session:
            await session.initialize()

            tools = (await session.list_tools()).tools
            names = {t.name for t in tools}
            for want in (
                "agent_submit_task", "agent_task_status", "agent_task_result",
                "agent_task_cancel", "agent_task_wait", "agent_task_steer",
                "agent_task_approve", "agent_task_list", "agent_task_file",
            ):
                assert want in names, f"tools/list missing {want}"
            status = next(t for t in tools if t.name == "agent_task_status")
            assert status.output_schema, "agent_task_status has no outputSchema"
            print(f"tools/list ok: {len(tools)} tools")

            unknown = await session.call_tool("agent_task_status", {"task_id": "nope"})
            assert unknown.is_error, "unknown task should be isError"
            print("unknown-task error semantics ok")

            sub = await session.call_tool(
                "agent_submit_task",
                {"prompt": "Reply with exactly: py-smoke-ok", "client_ref": "py-smoke-1"},
            )
            assert not sub.is_error, f"submit: {sub.content}"
            task_id = sub.structured_content["task_id"]
            print(f"submit ok: {task_id}")

            st = await session.call_tool("agent_task_status", {"task_id": task_id})
            s = st.structured_content["status"]
            assert s in ("working", "input_required", "completed", "failed", "cancelled"), s
            print(f"status ok: {s}")

            again = await session.call_tool(
                "agent_submit_task",
                {"prompt": "Reply with exactly: py-smoke-ok", "client_ref": "py-smoke-1"},
            )
            assert again.structured_content["deduplicated"] is True, "client_ref replay did not dedup"
            print("client_ref dedup ok")

            await session.call_tool("agent_task_cancel", {"task_id": task_id})
            print("cancel ok")
    print("PY-SMOKE-PASS")


asyncio.run(main())
