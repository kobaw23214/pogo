import type { AnswerReview } from "@/api/types";
import { gt } from "@/i18n";

export function validAnswerReview(value: unknown): value is AnswerReview {
  if (!value || typeof value !== "object") return false;
  const review = value as Partial<AnswerReview>;
  return (review.kind === "citation" || review.kind === "quote") &&
    ["running", "completed", "fallback", "cancelled"].includes(review.state ?? "") &&
    typeof review.turn === "number" && Number.isInteger(review.turn) && review.turn > 0 &&
    typeof review.started_at === "number" && Number.isFinite(review.started_at) && review.started_at > 0;
}

/** A disconnected/ended run must never leave an immortal running indicator. */
export function finishAnswerReview(review: AnswerReview, at: number, cancelled = false): AnswerReview {
  if (review.state !== "running") return review;
  return { ...review, state: cancelled ? "cancelled" : "fallback", finished_at: at,
    duration_ms: Math.max(0, at - review.started_at), reason: cancelled ? "cancelled" : "interrupted" };
}

export function answerReviewNoticeKind(text: string): AnswerReview["kind"] | null {
  if (text.startsWith("[pogo] citation check:")) return "citation";
  if (text.startsWith("[pogo] verbatim quotation check:") || text.startsWith("[pogo] quote check:")) return "quote";
  return null;
}

/** Old servers report a historical reminder, never proof a request is running. */
export function answerReviewNoticeText(text: string): string {
  const kind = answerReviewNoticeKind(text);
  return kind ? gt(kind === "citation" ? "answerReview.legacyCitation" : "answerReview.legacyQuote") : text;
}

export function showAnswerReviewNotice(text: string, turns: readonly { answerReview?: AnswerReview }[]): boolean {
  const kind = answerReviewNoticeKind(text);
  return !kind || !turns.some(turn => turn.answerReview?.kind === kind);
}
