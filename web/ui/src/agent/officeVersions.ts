import { officeRequest } from "@/api/client";
import type { OfficeSession } from "./officeTransport";

/** Saved-file revisions, separate from ONLYOFFICE's native undo stack. */
export interface OfficeFileVersion {
  id: string;
  fileRef: string;
  conversationId: string;
  path: string;
  fileType: "docx" | "xlsx";
  createdAt: string;
  committedAt?: string;
  operationId: string;
  batchId?: string;
  engine: "native" | "script" | "restore";
  actor: "agent" | "user";
  scriptDigest?: string;
  executionId?: string;
  /** Intent supplied by the caller; never presented as a verified diff. */
  summary?: string;
  restoredFrom?: string;
  recovered?: boolean;
  beforeVersion: string;
  afterVersion: string;
  beforeBytes: number;
  afterBytes: number;
  diff: {
    coverage: "cell-values-formulas" | "paragraph-text" | "unsupported";
    changeCount: number;
    changes: { kind: string; address?: string; sheet?: string; before?: string; after?: string }[];
    truncated: boolean;
    limitations: string[];
  };
  state: "committed";
}

export interface OfficeFileVersions {
  ok: boolean;
  fileRef: string;
  path: string;
  items: OfficeFileVersion[];
  nextCursor?: string;
  retention?: { maxVersions: number; maxBytes: number };
}

export interface OfficeVersionRestore {
  ok: boolean;
  state: string;
  batch_id: string;
  file_ref?: string;
  base_version?: string;
  disk_version?: string;
  versionId?: string;
  session?: OfficeSession;
}

export type OfficeRestoreSide = "before" | "after";
export type RestoreOfficeFileVersion = (versionId: string, expectedVersion: string, fileRef: string, restoreSide: OfficeRestoreSide) => Promise<OfficeVersionRestore>;

export const officeVersionsApi = {
  list(conversationId: string, path: string, cursor?: string, signal?: AbortSignal) {
    const query = new URLSearchParams({ conversation_id: conversationId, path, limit: "20" });
    if (cursor) query.set("cursor", cursor);
    return officeRequest<OfficeFileVersions>("GET", `/api/office/versions?${query}`, undefined, signal);
  },
  restore(conversationId: string, path: string, versionId: string, expectedVersion: string, operationId: string, restoreSide: OfficeRestoreSide = "after", signal?: AbortSignal) {
    return officeRequest<OfficeVersionRestore>("POST", "/api/office/versions/restore", { conversationId, path, versionId, expectedVersion, operationId, restoreSide }, signal);
  },
};
