import { describe, expect, it } from "vitest";
import { dateTimeInputToISO, localDateTimeInput, ScheduleDateTimeError } from "./scheduleDateTime";

describe("scheduled wall times", () => {
  it("round-trips a saved UTC instant through Shanghai without an eight-hour shift", () => {
    const original = "2026-10-20T01:00:00.000Z";
    const input = localDateTimeInput(original, "Asia/Shanghai");
    expect(input).toBe("2026-10-20T09:00");
    expect(dateTimeInputToISO(input, "Asia/Shanghai")).toBe(original);
  });

  it.each([
    ["UTC", "2026-10-20T01:00", "2026-10-20T01:00:00.000Z"],
    ["Asia/Kathmandu", "2026-10-20T09:00", "2026-10-20T03:15:00.000Z"],
    ["America/New_York", "2026-07-20T09:00", "2026-07-20T13:00:00.000Z"],
    ["America/New_York", "2026-12-20T09:00", "2026-12-20T14:00:00.000Z"],
  ])("uses %s rather than the machine zone", (zone, input, iso) => {
    expect(dateTimeInputToISO(input, zone)).toBe(iso);
    expect(localDateTimeInput(iso, zone)).toBe(input);
  });

  it.each([
    ["America/New_York", "2026-03-08T02:30", "nonexistent_time"],
    ["America/New_York", "2026-11-01T01:30", "ambiguous_time"],
    ["Australia/Lord_Howe", "2026-10-04T02:15", "nonexistent_time"],
    ["Australia/Lord_Howe", "2026-04-05T01:45", "ambiguous_time"],
  ])("rejects ambiguous or missing wall time %s %s", (zone, input, code) => {
    try { dateTimeInputToISO(input, zone); throw new Error("accepted invalid wall time"); }
    catch (err) { expect(err).toBeInstanceOf(ScheduleDateTimeError); expect((err as ScheduleDateTimeError).code).toBe(code); }
  });

  it.each(["2026-02-30T10:00", "2026-10-18T24:00", "2026-10-18T10:60", "invalid"])("rejects invalid calendar input %s", input => {
    expect(() => dateTimeInputToISO(input, "UTC")).toThrow(ScheduleDateTimeError);
  });
  it("reports invalid zones and preserves the empty input", () => {
    expect(() => dateTimeInputToISO("2026-10-20T09:00", "Invalid/Zone")).toThrow(ScheduleDateTimeError);
    expect(localDateTimeInput("", "UTC")).toBe("");
  });
});
