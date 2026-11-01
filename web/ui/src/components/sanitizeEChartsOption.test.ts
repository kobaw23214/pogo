import { describe, expect, it } from "vitest";
import { sanitizeEChartsOption } from "./sanitizeEChartsOption";

describe("sanitizeEChartsOption", () => {
  it("passes a plain valid option through with data intact", () => {
    const result = sanitizeEChartsOption(
      JSON.stringify({
        title: { text: "家族样本数" },
        xAxis: { type: "category", data: ["Emotet", "Mirai"] },
        yAxis: { type: "value" },
        series: [{ type: "bar", data: [120, 84] }],
      }),
    );
    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.option.series).toEqual([{ type: "bar", data: [120, 84] }]);
    }
  });

  it("rejects anything that is not JSON", () => {
    // The fence contract is JSON precisely so this rejects it: functions and
    // expressions are code, and option fields that ECharts would execute.
    expect(sanitizeEChartsOption('{"xAxis": {"formatter": (v) => v}}')).toEqual({ ok: false, reason: "json" });
    expect(sanitizeEChartsOption("// comment\n{}")).toEqual({ ok: false, reason: "json" });
  });

  it("rejects a non-object root", () => {
    expect(sanitizeEChartsOption("[1, 2, 3]")).toEqual({ ok: false, reason: "shape" });
    expect(sanitizeEChartsOption('"a string"')).toEqual({ ok: false, reason: "shape" });
  });

  it("drops fields ECharts renders as raw HTML or CSS", () => {
    const result = sanitizeEChartsOption(
      JSON.stringify({
        tooltip: { extraCssText: "background:url(javascript:alert(1))", formatter: "<b>{b}</b>" },
        toolbox: { feature: { dataView: { optionToContent: () => "x" } } },
        dataset: { transform: { type: "filter", config: { reg: "(a+)+$" } } },
      }),
    );
    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.option.tooltip).toEqual({});
      expect(result.option.toolbox).toEqual({ feature: {} });
      expect(result.option.dataset).toEqual({ transform: { type: "filter", config: {} } });
    }
  });

  it("keeps text-template formatters and drops markup formatters", () => {
    const result = sanitizeEChartsOption(
      JSON.stringify({
        a: { formatter: "{b}: {c}" },
        b: { formatter: "<script>alert(1)</script>{c}" },
        c: { formatter: "javascript:x" },
      }),
    );
    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.option.a).toEqual({ formatter: "{b}: {c}" });
      expect(result.option.b).toEqual({});
      expect(result.option.c).toEqual({});
    }
  });

  it("keeps http(s) links and drops other URL schemes", () => {
    const result = sanitizeEChartsOption(
      JSON.stringify({
        good: { link: "https://example.org/report" },
        protocol: { sublink: "javascript:alert(1)" },
        data: { link: "data:text/html,<script>x</script>" },
      }),
    );
    expect(result.ok).toBe(true);
    if (result.ok) {
      expect(result.option.good).toEqual({ link: "https://example.org/report" });
      expect(result.option.protocol).toEqual({});
      expect(result.option.data).toEqual({});
    }
  });

  it("refuses hostile sizes: huge strings, deep nesting, many nodes", () => {
    const hugeString = JSON.stringify({ title: { text: "x".repeat(21_000) } });
    expect(sanitizeEChartsOption(hugeString)).toEqual({ ok: false, reason: "budget" });

    let deep = "1";
    for (let i = 0; i < 100; i++) deep = `{"a":${deep}}`;
    expect(sanitizeEChartsOption(deep)).toEqual({ ok: false, reason: "budget" });

    const wide: unknown[] = [];
    for (let i = 0; i < 25_000; i++) wide.push(i);
    expect(sanitizeEChartsOption(JSON.stringify({ data: wide }))).toEqual({ ok: false, reason: "budget" });
  });

  it("handles real-world option shapes end to end", () => {
    const source = JSON.stringify({
      tooltip: { trigger: "axis", formatter: "{b}<br/>{c}" },
      legend: { data: ["样本量", "家族数"] },
      xAxis: { type: "category", data: ["1月", "2月", "3月"] },
      yAxis: { type: "value" },
      series: [
        { name: "样本量", type: "line", smooth: true, data: [10, 24, 18] },
        { name: "家族数", type: "bar", data: [3, 5, 4] },
      ],
    });
    const result = sanitizeEChartsOption(source);
    expect(result.ok).toBe(true);
    if (result.ok) {
      // "<br/>" in a tooltip formatter is markup; the rest survives intact.
      expect(result.option.tooltip).toEqual({ trigger: "axis" });
      expect((result.option.series as { length: number }).length).toBe(2);
    }
  });
});
