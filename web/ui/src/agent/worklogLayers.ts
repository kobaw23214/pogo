import { gt } from "@/i18n";
import type { TimelineCall, TurnItem } from "./timeline";
import { diffOf, formatDuration, shortPath, summarizeArgs } from "./timeline";
import type { BashDetails } from "@/api/types";
import type { Exchange } from "./exchanges";

// This module is deliberately free of Vue imports, like timeline.ts: a pure
// projection from a finished exchange into the work log's rows, unit tested
// without a component.

/**
 * The work log's rows: an exchange's work, layered.
 *
 * The flat TurnCard list answers "what happened"; the layered view answers
 * "what kind of work happened". Layers follow the grammar the research
 * converged on (Cursor's "Explored N tools", Codex's "Ran …"):
 *
 *   thinking  a turn's reasoning, one row per turn that has any
 *   explore   reads/searches/listings — the noisy majority, so consecutive
 *             ones cluster into a single row ("探索 · 读 a.ts 等 3 项")
 *   command   a bash call, always its own row, with exit and duration
 *   change    an edit/write call, always its own row, with the +A −R it made
 *   card      subagent and todo calls render as their own self-collapsing
 *             cards — wrapping a collapsing card in a collapsing row is two
 *             chevrons saying the same thing
 *   prose     a turn's text that is not the exchange's conclusion (the
 *             conclusion never enters the fold; it renders outside). One
 *             past breathing-band size folds behind its first line.
 *
 * Two presentation rules from the hierarchy research:
 *
 *   - The label is split into a verb and a detail, because they play different
 *     visual roles: the verb is the scan anchor (bold, stable position), the
 *     detail is the identifier (dim, mono). A single flat string cannot be
 *     styled that way — and a row without an identifier is the summary style
 *     that made Claude Code's v2.1.20 fold reviled.
 *   - A failed command opens its own row by default (GitHub Actions does the
 *     same with failed steps): the one row the reader is looking for is the
 *     red one, and making them click to find it is hostile.
 */
export interface WorkLogRow {
  id: string;
  kind: "thinking" | "prose" | "explore" | "command" | "change" | "card";
  /** thinking and prose rows. */
  turn?: TurnItem;
  /** prose rows. */
  text?: string;
  /** prose rows past the breathing-band size: folded behind their first line. */
  long?: boolean;
  /** explore rows hold the whole cluster; command/change/card hold one call. */
  calls?: TimelineCall[];
  /** The scan anchor, bold: 思考过程 / 探索 / 已执行 / 改动. */
  verb?: string;
  /** The identifier, dim: the path, the command, "等 3 项", the duration. */
  detail?: string;
  /** Command rows only: the un-clipped command. The opened row's label
   * completes into it in place — the body renders output only, so the command
   * never appears twice. */
  fullDetail?: string;
  /** Right-hand facts: "3.2s", "exit 1", "+82 −14". */
  meta?: string;
  failed?: boolean;
  /** Rows that open their own body on first paint — failed commands. */
  defaultOpen?: boolean;
}

const EXPLORE = new Set(["read", "ls", "find", "grep"]);

/** clip keeps the head of a command, which is where its identity is. */
function clip(s: string, max: number): string {
  const one = s.replace(/\s+/g, " ").trim();
  return one.length > max ? one.slice(0, max - 1) + "…" : one;
}

/** A prose row earns its keep as a one-breath aside ("好，先看文件："). Past
 * either bound it is the model drafting whole documents in visible text (GLM
 * does this), which folds behind its own first line and renders markdown when
 * opened — plain {{ text }} interpolation would show the syntax raw. */
const PROSE_LONG_CHARS = 400;
const PROSE_LONG_LINES = 6;

function isLongProse(text: string): boolean {
  return text.length > PROSE_LONG_CHARS || text.split("\n").length > PROSE_LONG_LINES;
}

/** The folded long-prose label: its first non-empty line, clipped. */
export function prosePreview(text: string): string {
  const first = text.split("\n").find((l) => l.trim() !== "") ?? "";
  return clip(first, 80);
}

function commandMeta(call: TimelineCall): { meta?: string; failed: boolean } {
  const d = call.result?.details as BashDetails | undefined;
  const failed = call.result?.is_error === true || (typeof d?.exit_code === "number" && d.exit_code !== 0) || (!!d?.status && d.status !== "running" && d.status !== "completed");
  const parts: string[] = [];
  if (typeof d?.duration_ms === "number" && Number.isFinite(d.duration_ms) && d.duration_ms >= 0 && !(d.background && (!d.status || d.status === "running"))) parts.push(formatDuration(d.duration_ms));
  if (typeof d?.exit_code === "number" && d.exit_code !== 0) parts.push(`exit ${d.exit_code}`);
  if (d?.background && (!d.status || d.status === "running")) parts.push(gt(d.status === "running" ? "bashResult.backgroundRunning" : "bashResult.backgroundDispatched"));
  return { meta: parts.join(" · ") || undefined, failed };
}

function changeMeta(call: TimelineCall): string | undefined {
  const d = diffOf(call.result?.details);
  const parts: string[] = [];
  if (d) parts.push(`+${d.added} −${d.removed}`);
  if (call.result?.duration_ms) parts.push(formatDuration(call.result.duration_ms));
  return parts.join(" · ") || undefined;
}

/** exploreMeta sums the cluster's call durations — the answer to "did the
 * reading cost anything" without opening the row. */
function exploreMeta(calls: TimelineCall[]): string | undefined {
  let total = 0;
  for (const c of calls) total += c.result?.duration_ms ?? 0;
  return total > 0 ? formatDuration(total) : undefined;
}

export function buildWorkLogRows(exchange: Exchange): WorkLogRow[] {
  const rows: WorkLogRow[] = [];

  for (const turn of exchange.turns) {
    if (turn.thinking.trim() !== "") {
      rows.push({
        id: `${turn.id}:thinking`,
        kind: "thinking",
        turn,
        verb: gt("workLog.thinking"),
        detail: turn.thinkingMs ? formatDuration(turn.thinkingMs) : undefined,
      });
    }

    for (const call of turn.calls) {
      if (EXPLORE.has(call.name)) {
        const last = rows[rows.length - 1];
        if (last?.kind === "explore") {
          last.calls!.push(call);
          last.failed = last.failed || call.result?.is_error === true;
          continue;
        }
        rows.push({
          id: `${call.callId}:explore`,
          kind: "explore",
          calls: [call],
          verb: gt("workLog.verbExplore"),
          failed: call.result?.is_error === true,
        });
        continue;
      }

      if (call.name === "bash") {
        const { meta, failed } = commandMeta(call);
        const command = summarizeArgs("bash", call.args);
        rows.push({
          id: call.callId,
          kind: "command",
          calls: [call],
          verb: gt("workLog.verbRan"),
          detail: clip(command, 60),
          // The raw command, newlines kept: collapsed the label is its clipped
          // head, opened the same label completes into this in place.
          fullDetail: command,
          meta,
          failed,
          // The failed step is the one the reader came for; open its body
          // rather than making them hunt (GitHub Actions' failed-step rule).
          defaultOpen: failed || undefined,
        });
        continue;
      }

      if (call.name === "edit" || call.name === "write") {
        rows.push({
          id: call.callId,
          kind: "change",
          calls: [call],
          verb: gt("workLog.verbChanged"),
          detail: shortPath(summarizeArgs(call.name, call.args)),
          meta: changeMeta(call),
          failed: call.result?.is_error === true,
        });
        continue;
      }

      // subagent and todo: cards that already manage their own collapse.
      rows.push({ id: call.callId, kind: "card", calls: [call], failed: call.result?.is_error === true });
    }

    // A settled turn's text that is not the conclusion is narration between
    // work ("好，先看文件：") — kept, muted, in place. Past the breathing-band
    // size it folds behind its first line instead of standing as a wall.
    if (turn !== exchange.answer && turn.text.trim() !== "") {
      const long = isLongProse(turn.text);
      rows.push({ id: `${turn.id}:prose`, kind: "prose", turn, text: turn.text, long: long || undefined });
    }
  }

  // Explore details are computed after clustering: the cluster's identity is
  // its first item plus how much followed it; its meta is the summed duration.
  for (const row of rows) {
    if (row.kind !== "explore") continue;
    const calls = row.calls!;
    const first = summarizeArgs(calls[0].name, calls[0].args);
    row.detail =
      calls.length === 1
        ? gt("workLog.exploreSummary", { detail: first })
        : gt("workLog.exploreSummaryMore", { detail: first, n: calls.length });
    row.meta = exploreMeta(calls);
  }

  return rows;
}
