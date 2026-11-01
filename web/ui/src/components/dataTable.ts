export type DataTableFormat = "csv" | "json";
export interface DataTableColumn { id: string; label: string }
export interface DataTableData { columns: DataTableColumn[]; rows: string[][] }
export type DataTableResult =
  | { ok: true; table: DataTableData }
  | { ok: false; reason: "empty" | "invalid" | "tooLarge" };

const MAX_SOURCE_BYTES = 256 * 1024;
const MAX_ROWS = 1_000;
const MAX_COLUMNS = 40;
const MAX_CELLS = 10_000;

function columns(labels: string[]): DataTableColumn[] {
  return labels.map((label, index) => ({ id: `c${index}`, label: label.trim() || `列 ${index + 1}` }));
}

function withinBudget(table: DataTableData): boolean {
  return table.columns.length > 0 && table.columns.length <= MAX_COLUMNS && table.rows.length <= MAX_ROWS && table.columns.length * Math.max(table.rows.length, 1) <= MAX_CELLS;
}

function parseCSV(source: string): DataTableResult {
  const rows: string[][] = [];
  let row: string[] = [];
  let field = "";
  let quoted = false;
  const input = source.replace(/^\uFEFF/, "");
  const finishRow = () => {
    row.push(field);
    if (row.some(value => value.trim() !== "")) rows.push(row);
    row = [];
    field = "";
  };
  for (let i = 0; i < input.length; i++) {
    const char = input[i];
    if (quoted) {
      if (char === '"') {
        if (input[i + 1] === '"') {
          field += '"';
          i++;
        } else {
          quoted = false;
        }
      } else {
        field += char;
      }
      continue;
    }
    if (char === '"' && field === "") {
      quoted = true;
    } else if (char === ",") {
      row.push(field);
      field = "";
    } else if (char === "\n" || char === "\r") {
      if (char === "\r" && input[i + 1] === "\n") i++;
      finishRow();
    } else {
      field += char;
    }
  }
  if (quoted) return { ok: false, reason: "invalid" };
  if (field !== "" || row.length) finishRow();
  if (!rows.length) return { ok: false, reason: "empty" };
  const labels = rows[0];
  if (!labels.length || labels.some(label => label.includes("\n") || label.includes("\r"))) return { ok: false, reason: "invalid" };
  const table: DataTableData = { columns: columns(labels), rows: [] };
  for (const values of rows.slice(1)) {
    if (values.length > labels.length) return { ok: false, reason: "invalid" };
    table.rows.push(Array.from({ length: labels.length }, (_, index) => values[index] ?? ""));
  }
  if (!withinBudget(table)) return { ok: false, reason: "tooLarge" };
  return { ok: true, table };
}

function cellText(value: unknown): string {
  if (value === null || value === undefined) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean" || typeof value === "bigint") return String(value);
  return JSON.stringify(value) ?? "";
}

function parseJSON(source: string): DataTableResult {
  let parsed: unknown;
  try {
    parsed = JSON.parse(source);
  } catch {
    return { ok: false, reason: "invalid" };
  }
  let labels: string[] = [];
  let values: unknown[] = [];
  if (Array.isArray(parsed)) {
    values = parsed;
    const keys = new Set<string>();
    for (const row of values) {
      if (typeof row !== "object" || row === null || Array.isArray(row)) return { ok: false, reason: "invalid" };
      Object.keys(row).forEach(key => keys.add(key));
    }
    labels = [...keys];
  } else if (typeof parsed === "object" && parsed !== null) {
    const candidate = parsed as { columns?: unknown; rows?: unknown };
    if (!Array.isArray(candidate.columns) || !candidate.columns.every(column => typeof column === "string") || !Array.isArray(candidate.rows)) {
      return { ok: false, reason: "invalid" };
    }
    labels = candidate.columns;
    values = candidate.rows;
  } else {
    return { ok: false, reason: "invalid" };
  }
  if (!values.length) return { ok: false, reason: "empty" };
  const table: DataTableData = { columns: columns(labels), rows: [] };
  for (const row of values) {
    if (Array.isArray(row)) {
      if (row.length > labels.length) return { ok: false, reason: "invalid" };
      table.rows.push(Array.from({ length: labels.length }, (_, index) => cellText(row[index])));
    } else if (typeof row === "object" && row !== null) {
      table.rows.push(labels.map(label => cellText((row as Record<string, unknown>)[label])));
    } else {
      return { ok: false, reason: "invalid" };
    }
  }
  if (!withinBudget(table)) return { ok: false, reason: "tooLarge" };
  return { ok: true, table };
}

export function parseDataTable(source: string, format: DataTableFormat): DataTableResult {
  if (!source.trim()) return { ok: false, reason: "empty" };
  if (new TextEncoder().encode(source).byteLength > MAX_SOURCE_BYTES) return { ok: false, reason: "tooLarge" };
  return format === "csv" ? parseCSV(source) : parseJSON(source);
}

function csvCell(value: string): string {
  return /[",\r\n]/.test(value) ? `"${value.replace(/"/g, '""')}"` : value;
}

export function serializeCSV(table: DataTableData, rows = table.rows): string {
  return [table.columns.map(column => csvCell(column.label)).join(","), ...rows.map(row => row.map(csvCell).join(","))].join("\r\n");
}
