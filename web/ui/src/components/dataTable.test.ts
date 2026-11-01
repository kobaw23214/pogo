import { describe, expect, it } from "vitest";
import { parseDataTable, serializeCSV } from "./dataTable";

describe("data table parsing", () => {
  it("parses quoted CSV cells, escaped quotes and embedded newlines", () => {
    const result = parseDataTable('name,notes\r\nAlpha,"a,b"\r\nBeta,"say ""hello""\nnext"\r\n', "csv");
    expect(result).toMatchObject({ ok: true });
    if (!result.ok) return;
    expect(result.table.columns.map(column => column.label)).toEqual(["name", "notes"]);
    expect(result.table.rows).toEqual([["Alpha", "a,b"], ["Beta", "say \"hello\"\nnext"]]);
  });

  it("accepts object arrays and explicit column/row JSON shapes", () => {
    const objects = parseDataTable('[{"name":"Alpha","value":2},{"name":"Beta","value":10,"extra":true}]', "json");
    expect(objects).toMatchObject({ ok: true });
    if (objects.ok) {
      expect(objects.table.columns.map(column => column.label)).toEqual(["name", "value", "extra"]);
      expect(objects.table.rows[1]).toEqual(["Beta", "10", "true"]);
    }
    const explicit = parseDataTable('{"columns":["name","value"],"rows":[["Alpha",2],["Beta",10]]}', "json");
    expect(explicit).toMatchObject({ ok: true });
  });

  it("rejects malformed and oversized input without rendering it", () => {
    expect(parseDataTable('a,b\n"unterminated', "csv")).toEqual({ ok: false, reason: "invalid" });
    expect(parseDataTable("a,b\n1,2,3", "csv")).toEqual({ ok: false, reason: "invalid" });
    expect(parseDataTable("[1,2,3]", "json")).toEqual({ ok: false, reason: "invalid" });
    expect(parseDataTable("x".repeat(257 * 1024), "csv")).toEqual({ ok: false, reason: "tooLarge" });
    expect(parseDataTable(`a\n${Array.from({ length: 1001 }, () => "1").join("\n")}`, "csv")).toEqual({ ok: false, reason: "tooLarge" });
  });

  it("exports valid CSV with quoting and CRLF rows", () => {
    const parsed = parseDataTable('name,notes\nAlpha,"a,b"', "csv");
    if (!parsed.ok) throw new Error("fixture did not parse");
    expect(serializeCSV(parsed.table)).toBe('name,notes\r\nAlpha,"a,b"');
    expect(parsed.table.rows[0][0]).toBe("Alpha");
  });

  it("keeps duplicate headers independent and stringifies nested values", () => {
    const csv = parseDataTable("name,name\nAlpha,Beta", "csv");
    if (!csv.ok) throw new Error("duplicate headers were rejected");
    expect(csv.table.columns.map(column => column.id)).toEqual(["c0", "c1"]);
    expect(csv.table.rows[0]).toEqual(["Alpha", "Beta"]);
    const json = parseDataTable('[{"name":"Alpha","meta":{"rank":2}}]', "json");
    if (!json.ok) throw new Error("nested JSON was rejected");
    expect(json.table.rows[0][1]).toBe('{"rank":2}');
  });

  it("classifies empty JSON tables as empty rather than oversized", () => {
    expect(parseDataTable("[]", "json")).toEqual({ ok: false, reason: "empty" });
    expect(parseDataTable('{"columns":["name"],"rows":[]}', "json")).toEqual({ ok: false, reason: "empty" });
  });
});
