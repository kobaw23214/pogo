import type { FileContent } from "@/api/types";

export type FileLineSelection =
  | { status: "none" }
  | { status: "available"; lineStart: number; lineEnd: number }
  | { status: "unavailable"; reason: "invalid" | "nonText" | "truncated" | "outOfRange" };

/** A line-limit response omits the separator after its last complete line.
 * If that line is empty, its trailing newline must still render as a row. */
export function preservePreviewTrailingLine(content: FileContent): boolean {
  return content.truncated === true && content.truncated_by === "lines";
}

/** Never clamp to another line: a citation beyond this preview is unavailable.
 * A byte-limited final line is incomplete and cannot represent full evidence. */
export function fileLineSelection(
  content: FileContent,
  lineStart?: number,
  lineEnd?: number,
): FileLineSelection {
  if (lineStart === undefined && lineEnd === undefined) return { status: "none" };
  const end = lineEnd ?? lineStart;
  if (lineStart === undefined || end === undefined || !Number.isSafeInteger(lineStart) || !Number.isSafeInteger(end) || lineStart < 1 || end < lineStart) {
    return { status: "unavailable", reason: "invalid" };
  }
  if (content.binary || content.text === undefined) return { status: "unavailable", reason: "nonText" };
  const text = content.text;
  const preservesLastLine = preservePreviewTrailingLine(content);
  const rows = text.length === 0 ? 0 : text.split("\n").length - (text.endsWith("\n") && !preservesLastLine ? 1 : 0);
  // Older responses can omit the truncation kind. Treat the last line as
  // incomplete unless a newline or an explicit line-limit response proves it.
  const completeRows = content.truncated && !preservesLastLine && !text.endsWith("\n") ? Math.max(0, rows - 1) : rows;
  if (end > completeRows) {
    return { status: "unavailable", reason: content.truncated ? "truncated" : "outOfRange" };
  }
  return { status: "available", lineStart, lineEnd: end };
}
