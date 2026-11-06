import { afterEach, describe, expect, it } from "vitest";
import { getLocale, setLocale, SUPPORTED_LOCALES } from "@/i18n";
import { sourceIdentity, sourcePreview } from "./citePreview";
import { sourceTypeName } from "./cite";
import { fmtRel } from "./fmtRel";
import { scheduleDate, scheduleTimezoneLabel } from "./schedulePresentation";
import { taskCenterDate } from "./taskCenterPresentation";
import { ScheduleDateTimeError } from "./scheduleDateTime";
import { prepareStarterFileParts } from "./starterDraftFiles";
import { fileComposerPart } from "./composerParts";
import type { CiteSource } from "@/api/types";
import { parseDataTable } from "@/components/dataTable";

afterEach(() => setLocale("zh-CN"));
const source: CiteSource & { content?: string } = {
  ts: 0, label: "S1", type: "command_exec", title: "", locator: "cmd://extract", call_id: "call", hash: "hash", excerpt: "",
  content: "===== Page 6 =====\nA recorded result 18.179.119.184:80",
};

describe("presentation follows the selected interface language", () => {
  it.each(SUPPORTED_LOCALES)("uses the selected %s locale for dates and relative time", locale => {
    setLocale(locale);
    const instant = Date.UTC(2026, 10, 5, 12, 30);
    expect(fmtRel(instant - 3600_000, instant)).toBe(new Intl.RelativeTimeFormat(locale, { style: "narrow" }).format(-1, "hour"));
    expect(scheduleDate(new Date(instant).toISOString(), "UTC")).toBe(new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short", timeZone: "UTC" }).format(new Date(instant)));
    expect(taskCenterDate(instant)).toBe(new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(new Date(instant)));
  });

  it("reformats sidebar relative time after a language switch", () => {
    const now = Date.UTC(2026, 10, 5);
    setLocale("zh-CN");
    const chinese = fmtRel(now - 3600_000, now);
    expect(chinese).toContain("小时前");
    setLocale("en");
    expect(getLocale()).toBe("en");
    expect(fmtRel(now - 3600_000, now)).toBe(new Intl.RelativeTimeFormat("en", { style: "narrow" }).format(-1, "hour"));
    expect(fmtRel(now - 3600_000, now)).not.toBe(chinese);
  });

  it("localizes citation chrome while preserving the recorded evidence", () => {
    setLocale("en");
    expect(sourceIdentity(source).title).toBe("Tool output");
    expect(sourceTypeName("information")).toBe("Information library");
    const english = sourcePreview(source, "18.179.119.184:80");
    expect(english.location).toBe("Page 6 (output marker)");
    setLocale("zh-CN");
    const chinese = sourcePreview(source, "18.179.119.184:80");
    expect(chinese.location).toBe("第 6 页（输出标记）");
    expect(chinese.text).toBe(english.text);
  });

  it("translates host timezone labels without changing their observed zone and offset", () => {
    setLocale("en");
    expect(scheduleTimezoneLabel("服务器本地时区 (CST UTC+08:00)")).toBe("Server local time zone (CST UTC+08:00)");
    expect(scheduleTimezoneLabel("Asia/Shanghai")).toBe("Asia/Shanghai");
    expect(scheduleTimezoneLabel("Local")).toBe("Local time zone");
    setLocale("zh-CN");
    expect(scheduleTimezoneLabel("服务器本地时区 (CST UTC+08:00)")).toBe("服务器本地时区 (CST UTC+08:00)");
  });

  it("shows schedule errors in English without changing stable error codes", () => {
    setLocale("en");
    const error = new ScheduleDateTimeError("invalid_timezone");
    expect(error.code).toBe("invalid_timezone");
    expect(error.message).toContain("Choose a valid IANA time zone");
    setLocale("zh-CN");
    expect(new ScheduleDateTimeError("invalid_timezone").message).toContain("请选择有效");
  });

  it("localizes attachment failures without uploading any partial draft", async () => {
    setLocale("en");
    const file = fileComposerPart({ key: "missing", label: "report.pdf", status: "uploading" });
    let uploads = 0;
    await expect(prepareStarterFileParts([file], new Map(), async () => { uploads++; return { path: "unexpected" }; }))
      .rejects.toThrow("File is not ready: report.pdf");
    expect(uploads).toBe(0);
  });

  it("localizes generated table column names while preserving user data", () => {
    setLocale("en");
    const english = parseDataTable(",value\nx,2", "csv");
    expect(english).toMatchObject({ ok: true, table: { columns: [{ id: "c0", label: "Column 1" }, { id: "c1", label: "value" }], rows: [["x", "2"]] } });
    setLocale("zh-CN");
    expect(parseDataTable(",value\nx,2", "csv")).toMatchObject({ ok: true, table: { columns: [{ id: "c0", label: "列 1" }, { id: "c1", label: "value" }], rows: [["x", "2"]] } });
  });
});
