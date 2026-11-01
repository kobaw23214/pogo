import type { OfficeReferenceSnapshot } from "@/api/types";
import type { InformationOfficePreview, InformationOfficeSheet } from "@/api/informationOffice";
export interface OfficeCellPoint { row: number; col: number }
export interface OfficeCellRange { start: OfficeCellPoint; end: OfficeCellPoint }
export const officeSelectionMaxBytes = 65536;
export function columnName(value: number): string {
  if (!Number.isSafeInteger(value) || value < 1 || value > 256) return "";
  let name = ""; while (value) { value--; name = String.fromCharCode(65 + value % 26) + name; value = Math.floor(value / 26); } return name;
}
function point(value: string): OfficeCellPoint | null {
  const match = /^\$?([A-Z]{1,2})\$?([1-9][0-9]{0,4})$/i.exec(value.trim()); if (!match) return null;
  let col = 0; for (const letter of match[1]!.toUpperCase()) col = col * 26 + letter.charCodeAt(0) - 64;
  const row = Number(match[2]); return col > 0 && col <= 256 && row <= 10000 ? { row, col } : null;
}
export function normalizeCellRange(range: OfficeCellRange): OfficeCellRange { return { start: { row: Math.min(range.start.row, range.end.row), col: Math.min(range.start.col, range.end.col) }, end: { row: Math.max(range.start.row, range.end.row), col: Math.max(range.start.col, range.end.col) } }; }
export function cellRangeName(range: OfficeCellRange): string { const r = normalizeCellRange(range), start = `${columnName(r.start.col)}${r.start.row}`, end = `${columnName(r.end.col)}${r.end.row}`; return start === end ? start : `${start}:${end}`; }
export function parseCellRange(value: string, sheet: InformationOfficeSheet): OfficeCellRange | null {
  const [first, last, extra] = value.trim().split(":"); if (!first || last !== undefined && !last.trim() || extra !== undefined) return null;
  const start = point(first), end = point(last || first); if (!start || !end || Math.max(start.row, end.row) > sheet.rows || Math.max(start.col, end.col) > sheet.columns) return null;
  return normalizeCellRange({ start, end });
}
export function officeCellMap(sheet: InformationOfficeSheet): Map<string, string> { return new Map(sheet.cells.map(cell => [`${cell.row}:${cell.col}`, cell.text])); }
export function selectedCellText(sheet: InformationOfficeSheet, range: OfficeCellRange, values?: Map<string, string>): string {
  const r = normalizeCellRange(range); if (r.start.row < 1 || r.start.col < 1 || r.end.row > sheet.rows || r.end.col > sheet.columns || (r.end.row - r.start.row + 1) * (r.end.col - r.start.col + 1) > 20000) throw new Error("selection-too-large");
  const map = values || officeCellMap(sheet), lines: string[] = []; let bytes = 0;
  for (let row = r.start.row; row <= r.end.row; row++) { const cells: string[] = []; for (let col = r.start.col; col <= r.end.col; col++) cells.push((map.get(`${row}:${col}`) || "").replace(/\r?\n/g, " ⏎ ").replace(/\t/g, " ")); const line = cells.join("\t"); bytes += new TextEncoder().encode(line).length + (lines.length ? 1 : 0); if (bytes > officeSelectionMaxBytes) throw new Error("selection-too-large"); lines.push(line); }
  return lines.join("\n");
}
export function informationOfficeSnapshot(preview: InformationOfficePreview, selected: { text: string; sheet?: string; range?: string; prefix?: string; suffix?: string }, capturedAt = new Date().toISOString()): OfficeReferenceSnapshot {
  const text = selected.text; if (!text.trim() || new TextEncoder().encode(text).length > officeSelectionMaxBytes) throw new Error("invalid-selection");
  return { path: preview.path, title: preview.title, editor: preview.editor, text, canvas_version: `disk:${preview.sha256}`, dirty: false, captured_at: capturedAt,
    information: { entry_id: preview.entry_id, version: preview.version, snapshot_sha256: preview.snapshot_sha256 },
    ...(preview.editor === "cell" ? { sheet: selected.sheet, range: selected.range } : { locator: { kind: "text", exact: text, ...(selected.prefix ? { prefix: selected.prefix.slice(-1024) } : {}), ...(selected.suffix ? { suffix: selected.suffix.slice(0, 1024) } : {}) } }) };
}
