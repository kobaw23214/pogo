import type { UserInputQuestion } from "@/api/types";

export interface UserInputDraft {
  selected: string[];
  other: boolean;
  custom: string;
}

export function emptyUserInputDraft(): UserInputDraft {
  return { selected: [], other: false, custom: "" };
}

export function hasUserInputAnswer(draft: UserInputDraft | undefined): boolean {
  return !!draft && (draft.selected.length > 0 || draft.other && !!draft.custom.trim());
}

/** Collect only the UI's choices; tool arguments and model text are never answers. */
export function collectUserInputAnswers(questions: UserInputQuestion[], drafts: Record<string, UserInputDraft>): { answers: Record<string, string[]>; missing: number } {
  const answers: Record<string, string[]> = {};
  let missing = -1;
  questions.forEach((question, index) => {
    const draft = drafts[question.id];
    if (!hasUserInputAnswer(draft)) {
      if (missing < 0) missing = index;
      return;
    }
    const selected = [...draft.selected];
    if (draft.other && draft.custom.trim()) selected.push(draft.custom.trim());
    answers[question.id] = selected;
  });
  return { answers, missing };
}
