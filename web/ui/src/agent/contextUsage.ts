import type { Message, ToolResult } from "@/api/types";
import { createContextEstimator } from "./contextEstimate";

export type ContextReadingSource = "measured" | "estimated" | "unknown";
export interface ContextReading {
  used: number;
  window: number;
  source: ContextReadingSource;
  /** Unclamped for the numeric label; a value over 100 is useful information. */
  percent: number | null;
  free: number | null;
}
interface ContextInput {
  used: number;
  window: number;
  overhead: number;
  /** The retained count belongs to a previous model and is only approximate. */
  estimated?: boolean;
  messages: Message[];
  results: Record<string, ToolResult>;
}

const count = (value: number) => Number.isFinite(value) ? Math.max(0, Math.floor(value)) : 0;

/** One reading for the slash menu, selected command and context meter.
 * The API's context_tokens is a single provider prompt, never usage.input's
 * cumulative bill. Zero means unmeasured, as happens after reopening a session
 * or replacing history. Only then do we estimate; an overhead estimate must
 * never inflate a nonzero authoritative provider measurement.
 */
export function createContextUsageReader() {
  const estimator = createContextEstimator();
  return (input: ContextInput): ContextReading => {
    const measured = count(input.used);
    const window = count(input.window);
    let used = measured;
    let source: ContextReadingSource = input.estimated ? "estimated" : "measured";
    if (!measured) {
      used = count(input.overhead);
      for (const message of input.messages) used += estimator.ofMessage(message).tokens;
      for (const result of Object.values(input.results)) used += estimator.ofResult(result);
      source = used > 0 ? "estimated" : "unknown";
    }
    return {
      used, window, source,
      percent: source !== "unknown" && window > 0 ? used / window * 100 : null,
      free: source !== "unknown" && window > 0 ? Math.max(window - used, 0) : null,
    };
  };
}

/** SVG percentages stay bounded; unknown readings draw the neutral track only. */
export function contextRingPercent(value: number | null | undefined): number | null {
  return value == null || !Number.isFinite(value) ? null : Math.min(100, Math.max(0, value));
}
