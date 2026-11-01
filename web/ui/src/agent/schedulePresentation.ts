import type { InputLayout, MessageInputPart, ScheduleJob, ScheduleJobCreate, ScheduleRunRecord } from "@/api/types";
import { browserTimeZone, dateTimeInputToISO, localDateTimeInput } from "./scheduleDateTime";

export interface ScheduleDraft { key: string; title?: string; prompt: string; parts: MessageInputPart[]; input_layout: InputLayout; source_session_id?: string; agent_id?: string; model?: string }
export type ScheduleFrequency = "once" | "hourly" | "daily" | "weekdays" | "weekly" | "monthly" | "interval" | "custom";
export interface ScheduleTiming { frequency: ScheduleFrequency; time: string; minute: number; weekdays: number[]; monthDay: number; every: number; unit: "minutes" | "hours" | "days"; at: string; cron: string; timezone: string }
type Translate = (key: string, values?: Record<string, string | number>) => string;
export function newScheduleTiming(): ScheduleTiming { return { frequency: "daily", time: "09:00", minute: 0, weekdays: [1], monthDay: 1, every: 1, unit: "hours", at: "", cron: "", timezone: browserTimeZone() }; }
export function timingFromJob(job: ScheduleJob): ScheduleTiming {
  const value = newScheduleTiming(); value.timezone = job.timezone || "";
  if (job.kind === "at") { value.frequency = "once"; value.at = job.at ? localDateTimeInput(job.at, job.timezone || job.effective_timezone || browserTimeZone()) : ""; return value; }
  if (job.kind === "every") { value.frequency = "interval"; const seconds = job.every_sec || 60; value.unit = seconds % 86400 === 0 ? "days" : seconds % 3600 === 0 ? "hours" : "minutes"; value.every = seconds / (value.unit === "days" ? 86400 : value.unit === "hours" ? 3600 : 60); return value; }
  const cron = job.cron || ""; value.cron = cron;
  const daily = /^(\d{1,2}) (\d{1,2}) \* \* (\*|1-5|[0-7](?:,[0-7])*)$/.exec(cron);
  const hourly = /^(\d{1,2}) \* \* \* \*$/.exec(cron), monthly = /^(\d{1,2}) (\d{1,2}) (\d{1,2}) \* \*$/.exec(cron);
  if (daily) { value.time = `${daily[2].padStart(2, "0")}:${daily[1].padStart(2, "0")}`; value.frequency = daily[3] === "*" ? "daily" : daily[3] === "1-5" ? "weekdays" : "weekly"; if (value.frequency === "weekly") value.weekdays = [...new Set(daily[3].split(",").map(day => Number(day) % 7))]; }
  else if (hourly) { value.frequency = "hourly"; value.minute = Number(hourly[1]); }
  else if (monthly) { value.frequency = "monthly"; value.time = `${monthly[2].padStart(2, "0")}:${monthly[1].padStart(2, "0")}`; value.monthDay = Number(monthly[3]); }
  else value.frequency = "custom";
  return value;
}
export function timingPayload(value: ScheduleTiming, fallbackZone = browserTimeZone()): Pick<ScheduleJobCreate, "kind" | "cron" | "at" | "every_sec" | "timezone"> {
  const timezone = value.timezone;
  if (timezone) new Intl.DateTimeFormat("en", { timeZone: timezone }).format();
  if (value.frequency === "once") { if (!value.at) throw new Error("scheduleDialog.onceRequired"); return { kind: "at", at: dateTimeInputToISO(value.at, timezone || fallbackZone), timezone }; }
  if (value.frequency === "interval") { if (!Number.isFinite(value.every) || value.every < 1 || !Number.isInteger(value.every)) throw new Error("scheduleDialog.intervalInvalid"); return { kind: "every", every_sec: value.every * (value.unit === "days" ? 86400 : value.unit === "hours" ? 3600 : 60), timezone }; }
  if (value.frequency === "custom") { if (!value.cron.trim()) throw new Error("scheduleDialog.cronRequired"); return { kind: "cron", cron: value.cron.trim(), timezone }; }
  const time = /^(\d{2}):(\d{2})$/.exec(value.time); if (!time || +time[1] > 23 || +time[2] > 59) throw new Error("scheduleDialog.timeInvalid");
  const minute = +time[2], hour = +time[1]; let cron = `${minute} ${hour} * * *`;
  if (value.frequency === "hourly") { if (!Number.isInteger(value.minute) || value.minute < 0 || value.minute > 59) throw new Error("scheduleDialog.timeInvalid"); cron = `${value.minute} * * * *`; }
  if (value.frequency === "weekdays") cron = `${minute} ${hour} * * 1-5`;
  if (value.frequency === "weekly") { const days = [...new Set(value.weekdays)].sort(); if (!days.length || days.some(day => !Number.isInteger(day) || day < 0 || day > 6)) throw new Error("scheduleDialog.weekdaysRequired"); cron = `${minute} ${hour} * * ${days.join(",")}`; }
  if (value.frequency === "monthly") { if (!Number.isInteger(value.monthDay) || value.monthDay < 1 || value.monthDay > 31) throw new Error("scheduleDialog.monthDayInvalid"); cron = `${minute} ${hour} ${value.monthDay} * *`; }
  return { kind: "cron", cron, timezone };
}
export function scheduleState(job: ScheduleJob, now = Date.now()): string {
  if (job.waiting_human) return "waiting";
  if (job.running) return "running";
  if (job.expires_at && new Date(job.expires_at).getTime() <= now) return "expired";
  if (job.kind === "at" && job.last_run_status === "ok" && !job.next_run) return "ok";
  if (!job.enabled) return (job.consecutive_failures || 0) >= 10 ? "autoPaused" : "paused";
  if (job.availability_error) return "unavailable";
  if (job.last_run_status === "error") return "error";
  if (job.last_run_status === "skipped") return "skipped";
  return job.kind === "at" && job.last_run_status === "ok" ? "ok" : "ready";
}
export function scheduleNeedsAttention(job: ScheduleJob): boolean { return ["waiting", "unavailable", "error", "autoPaused"].includes(scheduleState(job)); }
export function scheduleTitle(job: ScheduleJob): string { return job.title?.trim() || job.id; }
export function scheduleDate(iso: string | undefined | null, zone: string | undefined, locale = "zh-CN"): string {
  if (!iso || iso.startsWith("0001-")) return "—";
  try { return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short", ...(zone ? { timeZone: zone } : {}) }).format(new Date(iso)); } catch { return iso; }
}
export function describeSchedule(job: ScheduleJob, t: Translate): string {
  const v = timingFromJob(job), time = v.time;
  if (v.frequency === "once") return t("scheduleDialog.onceAt", { time: scheduleDate(job.at, job.timezone || job.effective_timezone) });
  if (v.frequency === "daily") return t("scheduleDialog.dailyAt", { time });
  if (v.frequency === "weekdays") return t("scheduleDialog.weekdaysAt", { time });
  if (v.frequency === "weekly") return t("scheduleDialog.weeklyDays", { days: v.weekdays.map(day => t(`scheduleDialog.dow${day}`)).join("、"), time });
  if (v.frequency === "monthly") return t("scheduleDialog.monthlyAt", { day: v.monthDay, time });
  if (v.frequency === "hourly") return t("scheduleDialog.hourlyAt", { m: v.minute });
  if (v.frequency === "interval") return t(`scheduleDialog.every${v.unit}`, { n: v.every });
  return t("scheduleDialog.customSummary", { cron: job.cron || "" });
}
export function runState(run: ScheduleRunRecord): string { return run.end_reason === "waiting_human" ? "waiting" : run.status; }
