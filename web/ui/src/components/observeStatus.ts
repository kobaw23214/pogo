// Types and small pure helpers for the shared collection-status card
// (ObserveStatusCard.vue) shown on both monitor sheets. The shape mirrors
// GET /api/observability/status. The card reports metadata export and
// sampling counters only — endpoints, headers and secrets are never part
// of the status payload and never rendered.

export interface OtlpStatus {
  enabled: boolean;
  protocol?: string;
  queued: number;
  queue_capacity: number;
  exported: number;
  failed: number;
  dropped: number;
  rejected: number;
  sampled_out: number;
  invalid: number;
  last_error_kind?: string;
  last_success_at?: number;
}

export interface ObservabilityStatus {
  mcp_enabled: boolean;
  skills_enabled: boolean;
  history_enabled: boolean;
  heartbeat_errors: number;
  otlp_config_error: boolean;
  otlp: OtlpStatus;
}

// A counter is shown only when it is a finite non-negative number; a backend
// that omits or corrupts a field yields "—", never a fabricated 0.
export function counterText(v: number | null | undefined): string | null {
  if (v === null || v === undefined || !Number.isFinite(v) || v < 0) return null;
  return String(Math.floor(v));
}

// The bridge the main api object must satisfy for the status card. Kept
// structural (no import of client.ts) to avoid a circular dependency.
export interface ObservabilityStatusBridge {
  observabilityStatus(): Promise<ObservabilityStatus>;
}

export function asObservabilityBridge(candidate: unknown): ObservabilityStatusBridge {
  const a = candidate as Partial<ObservabilityStatusBridge> | null | undefined;
  if (a && typeof a.observabilityStatus === "function") {
    return a as ObservabilityStatusBridge;
  }
  throw new Error("observability status api not wired: api.observabilityStatus missing");
}
