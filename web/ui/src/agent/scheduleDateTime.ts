export class ScheduleDateTimeError extends Error {
  constructor(public readonly code: "invalid_timezone" | "invalid_date" | "nonexistent_time" | "ambiguous_time") {
    super({
      invalid_timezone: "请选择有效的 IANA 时区，例如 Asia/Shanghai。",
      invalid_date: "请选择有效的日期和时间。",
      nonexistent_time: "该当地时间因夏令时调整而不存在，请重新选择。",
      ambiguous_time: "该当地时间因夏令时调整会出现两次，请选择其他时间。",
    }[code]);
    this.name = "ScheduleDateTimeError";
  }
}

export function browserTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

function formatter(timeZone: string): Intl.DateTimeFormat {
  try {
    return new Intl.DateTimeFormat("en-CA", {
      timeZone, year: "numeric", month: "2-digit", day: "2-digit",
      hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23",
    });
  } catch {
    throw new ScheduleDateTimeError("invalid_timezone");
  }
}

function fields(format: Intl.DateTimeFormat, timestamp: number): Record<string, number> {
  return Object.fromEntries(format.formatToParts(timestamp).filter(p => p.type !== "literal").map(p => [p.type, Number(p.value)]));
}

/** A datetime-local value is wall time in the selected zone, never sliced UTC. */
export function localDateTimeInput(iso: string, timeZone = browserTimeZone()): string {
  if (!iso) return "";
  const timestamp = Date.parse(iso);
  if (!Number.isFinite(timestamp)) throw new ScheduleDateTimeError("invalid_date");
  const p = fields(formatter(timeZone), timestamp);
  const two = (n: number) => String(n).padStart(2, "0");
  return `${p.year}-${two(p.month)}-${two(p.day)}T${two(p.hour)}:${two(p.minute)}`;
}

/** Resolve wall time without relying on the browser/server's local zone.
 * DST gaps and overlaps are rejected rather than silently moved by Date. */
export function dateTimeInputToISO(value: string, timeZone = browserTimeZone()): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(value);
  if (!match) throw new ScheduleDateTimeError("invalid_date");
  const [year, month, day, hour, minute] = match.slice(1).map(Number);
  const naive = Date.UTC(year, month - 1, day, hour, minute);
  const parsed = new Date(naive);
  if (year < 1000 || parsed.getUTCFullYear() !== year || parsed.getUTCMonth() !== month - 1 || parsed.getUTCDate() !== day || parsed.getUTCHours() !== hour || parsed.getUTCMinutes() !== minute) {
    throw new ScheduleDateTimeError("invalid_date");
  }
  const format = formatter(timeZone);
  const offsets = new Set<number>();
  // Include both sides of transitions, including half-hour DST and quarter-hour zones.
  for (const hours of [-36, -24, -12, 0, 12, 24, 36]) {
    const sample = naive + hours * 3_600_000;
    const p = fields(format, sample);
    offsets.add(Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute, p.second) - sample);
  }
  const matches: number[] = [];
  for (const offset of offsets) {
    const candidate = naive - offset;
    const p = fields(format, candidate);
    if (p.year === year && p.month === month && p.day === day && p.hour === hour && p.minute === minute && p.second === 0) matches.push(candidate);
  }
  if (matches.length === 0) throw new ScheduleDateTimeError("nonexistent_time");
  if (matches.length !== 1) throw new ScheduleDateTimeError("ambiguous_time");
  return new Date(matches[0]).toISOString();
}
