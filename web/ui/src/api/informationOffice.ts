import { authHeaders } from "./client";
import { InformationAPIError } from "./information";

export interface InformationOfficeCell { row: number; col: number; text: string }
export interface InformationOfficeSheet { name: string; rows: number; columns: number; cells: InformationOfficeCell[] }
export interface InformationOfficePreview {
  entry_id: string; version: number; sha256: string; snapshot_sha256: string; path: string; title: string;
  editor: "word" | "cell"; paragraphs?: string[]; sheets?: InformationOfficeSheet[]; truncated: boolean;
}
export function isInformationOfficePreview(value: unknown): value is InformationOfficePreview {
  if (!value || typeof value !== "object") return false;
  const v = value as InformationOfficePreview;
  if (!/^inf_[a-f0-9]{32}$/.test(v.entry_id) || !Number.isSafeInteger(v.version) || v.version < 1 || !/^[a-f0-9]{64}$/.test(v.sha256) || !/^[a-f0-9]{64}$/.test(v.snapshot_sha256) || typeof v.path !== "string" || !v.path.startsWith(`information-assets/${v.sha256}/`) || !/^information-assets\/[a-f0-9]{64}\/[^/\\\u0000]+$/.test(v.path) || typeof v.title !== "string" || !v.title.trim() || typeof v.truncated !== "boolean") return false;
  if (v.editor === "word") return Array.isArray(v.paragraphs) && v.paragraphs.length <= 20000 && v.paragraphs.every(p => typeof p === "string");
  if (v.editor !== "cell" || !Array.isArray(v.sheets) || !v.sheets.length || v.sheets.length > 64) return false;
  let count = 0;
  return v.sheets.every(sheet => typeof sheet.name === "string" && Number.isSafeInteger(sheet.rows) && sheet.rows > 0 && sheet.rows <= 10000 && Number.isSafeInteger(sheet.columns) && sheet.columns > 0 && sheet.columns <= 256 && Array.isArray(sheet.cells) && (count += sheet.cells.length) <= 100000 && sheet.cells.every(c => Number.isSafeInteger(c.row) && c.row > 0 && c.row <= sheet.rows && Number.isSafeInteger(c.col) && c.col > 0 && c.col <= sheet.columns && typeof c.text === "string"));
}
export async function informationOfficePreview(id: string, version: number, signal?: AbortSignal): Promise<InformationOfficePreview> {
  if (!/^inf_[a-f0-9]{32}$/.test(id) || !Number.isSafeInteger(version) || version < 1) throw new Error("Invalid information Office version");
  const response = await fetch(`/api/information/entries/${encodeURIComponent(id)}/office-preview?version=${version}`, { headers: authHeaders(), credentials: "same-origin", cache: "no-store", signal });
  let value: unknown; try { value = await response.json(); } catch { value = null; }
  if (!response.ok) throw new InformationAPIError((value as { error?: string } | null)?.error || response.statusText, response.status);
  if (!isInformationOfficePreview(value) || value.entry_id !== id || value.version !== version) throw new Error("Invalid information Office preview response");
  return value;
}
