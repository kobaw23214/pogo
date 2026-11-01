import type { InjectionKey, Ref } from "vue";

/** Shared by the persistent approval bar and any inline copy of its card. */
export interface ApprovalUiState {
  pending: Ref<Record<string, boolean>>;
  errors: Ref<Record<string, string>>;
  activeGateIds: Readonly<Ref<ReadonlySet<string>>>;
  serverOffset: Readonly<Ref<number | null>>;
  canDecide: boolean;
  sessionId: Readonly<Ref<string | null>>;
}

export const approvalUiKey: InjectionKey<ApprovalUiState> = Symbol("approval-ui");
