// Parse a captured SSE response body into display frames. The server stores
// the raw text (resp_body); turning it into per-event cards is purely a view
// concern, so this lives beside the component that renders it.
//
// SSE wire rules applied here: frames are separated by blank lines; a frame
// is a set of `field: value` lines where data may span multiple `data:` lines
// (joined with \n); a missing event field means "message". The capture has a
// size cap, so the tail frame may be cut mid-line — it is still returned,
// flagged partial, so the panel can say so instead of silently dropping it.

export interface SseFrame {
  event: string;
  data: string;
  /** True when the frame never saw its terminating blank line (capture cap). */
  partial: boolean;
  /** Parsed data when it is valid JSON (JSON-RPC payloads are). */
  json?: unknown;
}

export function looksLikeSse(text: string): boolean {
  return /(^|\n)(event|data):/.test(text);
}

export function parseSseFrames(text: string): SseFrame[] {
  const frames: SseFrame[] = [];
  // Keep the trailing segment even when it lacks the blank terminator.
  const blocks = text.split(/\n\s*\n/);
  blocks.forEach((block, i) => {
    if (!block.trim()) return;
    let event = "message";
    const dataLines: string[] = [];
    for (const line of block.split("\n")) {
      const m = /^(event|data)\s*:\s?(.*)$/.exec(line);
      if (!m) continue; // id:/retry:/comments — not displayed
      if (m[1] === "event") event = m[2].trim() || "message";
      else dataLines.push(m[2]);
    }
    const data = dataLines.join("\n");
    if (!data && event === "message" && block.trim().startsWith(":")) return; // keepalive comment
    const frame: SseFrame = { event, data, partial: i === blocks.length - 1 && !/\n\s*\n\s*$/.test(text) };
    try {
      frame.json = JSON.parse(data);
    } catch {
      /* non-JSON data renders as text */
    }
    frames.push(frame);
  });
  return frames;
}

// One-line JSON-RPC summary for a frame's data: the "what is this message"
// line above the tree, the Inspector-style classification.
export type FrameKind = "response" | "error" | "notification" | "request" | "text";

export interface FrameSummary {
  kind: FrameKind;
  id?: number | string;
  method?: string;
  message?: string; // error.message, when present
}

export function summarizeFrame(f: SseFrame): FrameSummary {
  const j = f.json as Record<string, unknown> | undefined;
  if (!j || typeof j !== "object") return { kind: "text" };
  const has = (k: string) => j[k] !== undefined;
  if (has("method") && has("id")) return { kind: "request", id: j.id as number | string, method: String(j.method) };
  if (has("method")) return { kind: "notification", method: String(j.method) };
  if (has("error")) {
    const err = j.error as { message?: string } | null;
    return { kind: "error", id: j.id as number | string | undefined, message: err?.message };
  }
  if (has("result")) return { kind: "response", id: j.id as number | string | undefined };
  return { kind: "text" };
}
