import { describe, expect, it } from "vitest";
import { useAgentStream } from "./useAgentStream";
import { buildTimeline } from "./timeline";
import type { AgentEvent } from "@/api/types";

// The live-sync fold: attachment blocks surviving the user_message event,
// optimistic bubbles retiring on the server's echo, and uploads landing
// without a message. Hand-written events, in the style of replay.test.ts.

function ev(partial: Partial<AgentEvent> & Pick<AgentEvent, "type">): AgentEvent {
  return { seq: 0, ts: 1, ...partial };
}

describe("user_message content", () => {
  it("keeps the attachment blocks the event carries, like the server's fold", () => {
    const s = useAgentStream();
    s.apply(
      ev({
        type: "user_message",
        message_id: "u1",
        text: "看下这个文件",
        content: [
          { type: "file", file_path: "uploads/请求.txt", file_name: "请求.txt" },
          { type: "text", text: "看下这个文件" },
        ],
      }),
    );

    const items = buildTimeline(s.messages.value, s.results.value, s.live.value);
    const user = items.find((i) => i.kind === "user");
    expect(user && user.kind === "user" ? user.files : []).toEqual([
      { path: "uploads/请求.txt", name: "请求.txt" },
    ]);
  });

  it("falls back to the compact text form when the event has no content", () => {
    const s = useAgentStream();
    s.apply(ev({ type: "user_message", message_id: "u1", text: "hello" }));
    expect(s.messages.value[0].content).toEqual([{ type: "text", text: "hello" }]);
  });
});

describe("optimistic user bubbles", () => {
  const placeholder = {
    id: "c-test",
    role: "user" as const,
    content: [
      { type: "image" as const, image_ref: "uploads/a.png", image_name: "a.png" },
      { type: "text" as const, text: "图里有什么" },
    ],
    ts: 1,
  };

  it("retires the placeholder when the server echoes its client id", () => {
    const s = useAgentStream();
    s.sendOptimistic(placeholder);
    expect(s.pendingUser.value).toHaveLength(1);

    s.apply(
      ev({
        type: "user_message",
        message_id: "u3",
        client_id: "c-test",
        text: "图里有什么",
        content: placeholder.content,
      }),
    );

    expect(s.pendingUser.value).toHaveLength(0);
    expect(s.messages.value).toHaveLength(1);
    expect(s.messages.value[0].id).toBe("u3");
    expect(s.messages.value[0].content).toEqual(placeholder.content);
  });

  it("leaves the placeholder when another tab's message arrives without the id", () => {
    const s = useAgentStream();
    s.sendOptimistic(placeholder);
    s.apply(ev({ type: "user_message", message_id: "u1", text: "from elsewhere" }));

    expect(s.pendingUser.value).toHaveLength(1);
    expect(s.messages.value).toHaveLength(1);
  });

  it("dropOptimistic removes the placeholder of a failed send", () => {
    const s = useAgentStream();
    s.sendOptimistic(placeholder);
    s.dropOptimistic("c-test");
    expect(s.pendingUser.value).toHaveLength(0);
  });

  it("a snapshot does not swallow a send still in flight", () => {
    const s = useAgentStream();
    s.sendOptimistic(placeholder);
    s.apply(
      ev({
        type: "snapshot",
        snapshot: {
          seq: 9,
          messages: [{ id: "u1", role: "user", content: [{ type: "text", text: "old" }] }],
          results: {},
          live: {
            active: false,
            pending_tools: [],
            pending_gates: [],
            pending_user_inputs: [],
            incoming: [],
          },
          run: { active: false },
          policy: { mode: "standard" },
          usage: { input: 0, output: 0 },
          bg_tasks: [],
          context_tokens: 0,
        },
      }),
    );

    expect(s.messages.value).toHaveLength(1);
    expect(s.pendingUser.value).toHaveLength(1);
  });
});

describe("attachment_uploaded", () => {
  const meta = {
    path: "uploads/报告.docx",
    name: "报告.docx",
    kind: "word",
    mime: "application/octet-stream",
    size: 10,
    created: "2026-08-23T00:00:00Z",
  };

  it("files the upload and dedupes by path", () => {
    const s = useAgentStream();
    s.apply(ev({ type: "attachment_uploaded", attachment: meta }));
    s.apply(ev({ type: "attachment_uploaded", attachment: meta }));
    s.apply(
      ev({
        type: "attachment_uploaded",
        attachment: { ...meta, path: "uploads/表.xlsx", name: "表.xlsx", kind: "excel" },
      }),
    );

    expect(s.uploads.value.map((u) => u.path)).toEqual(["uploads/报告.docx", "uploads/表.xlsx"]);
  });
});

describe("image_describe events", () => {
  const userMsg = {
    id: "u1",
    role: "user" as const,
    content: [{ type: "image" as const, image_ref: "uploads/a.png", image_name: "a.png" }],
    ts: 1,
  };

  function withImageMessage(): ReturnType<typeof useAgentStream> {
    const s = useAgentStream();
    s.apply(ev({ type: "user_message", message_id: "u1", content: userMsg.content }));
    return s;
  }

  it("folds start then end, and patches the description into the image alt", () => {
    const s = withImageMessage();
    s.apply(ev({ type: "image_describe_start", image_ref: "uploads/a.png", image_name: "a.png" }));
    expect(s.describes.value).toEqual([
      { ref: "uploads/a.png", name: "a.png", status: "running", startedAt: 1 },
    ]);

    s.apply(
      ev({
        type: "image_describe_end",
        image_ref: "uploads/a.png",
        text: "一张蓝色矩形截图",
        duration_ms: 12300,
      }),
    );
    expect(s.describes.value[0]).toMatchObject({ status: "done", text: "一张蓝色矩形截图", durationMs: 12300 });

    // The bubble can offer the description without waiting for a snapshot.
    const items = buildTimeline(s.messages.value, s.results.value, s.live.value);
    const user = items.find((i) => i.kind === "user");
    expect(user && user.kind === "user" ? user.imageAlts : undefined).toEqual({
      "uploads/a.png": "一张蓝色矩形截图",
    });
  });

  it("a failed describe keeps the failure and patches no alt", () => {
    const s = withImageMessage();
    s.apply(ev({ type: "image_describe_start", image_ref: "uploads/a.png", image_name: "a.png" }));
    s.apply(ev({ type: "image_describe_end", image_ref: "uploads/a.png", error: "视觉服务不可用或未配置", duration_ms: 120000 }));

    expect(s.describes.value[0]).toMatchObject({ status: "failed", error: "视觉服务不可用或未配置" });
    const block = s.messages.value[0].content[0];
    expect(block.type === "image" ? block.image_alt : "set").toBeUndefined();
  });

  it("run_end retires the step; the alt stays on the message", () => {
    const s = withImageMessage();
    s.apply(ev({ type: "image_describe_start", image_ref: "uploads/a.png", image_name: "a.png" }));
    s.apply(ev({ type: "image_describe_end", image_ref: "uploads/a.png", text: "描述", duration_ms: 5 }));
    s.apply(ev({ type: "run_end" }));

    expect(s.describes.value).toHaveLength(0);
    const block = s.messages.value[0].content[0];
    expect(block.type === "image" ? block.image_alt : undefined).toBe("描述");
  });
});
