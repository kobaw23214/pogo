/** Metadata discovery is read-only. These values are evidence for display,
 * never a replacement for the user's configured context or output budget. */
export type ModelMetadataPreset = "vllm" | "ollama-running" | "ollama-show" | "llamacpp";

export interface ModelMetadataRow {
  model: string;
  contextWindow?: number;
  maxOutputTokens?: number;
  source: string;
  kind: "runtime" | "declared" | "configured";
}

export interface ModelMetadataRequest {
  method: "GET" | "POST";
  url: string;
  body?: { model: string };
}

/** Keep path prefixes verbatim: a gateway's /tenant/compatible/v1 is not /v1.
 * The backend applies the same fixed endpoint mapping and validates again. */
export function modelMetadataRequest(baseURL: string, model: string, preset: ModelMetadataPreset): ModelMetadataRequest {
  const base = baseURL.trim().replace(/\/+$/, "");
  let parsed: URL;
  try { parsed = new URL(base); } catch { throw new Error("Invalid metadata Base URL"); }
  const authority = /^https?:\/\/([^/]+)(?:\/|$)/.exec(base)?.[1];
  if (!authority || authority.includes("@") || !parsed.hostname || parsed.username || parsed.password || /[?#\\\s\u0000-\u0020\u007f-\u009f]/.test(base)) {
    throw new Error("Metadata Base URL must be an absolute HTTP(S) URL without credentials, query or fragment");
  }
  if (preset === "vllm") return { method: "GET", url: `${base}/models` };
  const nativeBase = base.replace(/\/v1$/, "");
  if (preset === "llamacpp") return { method: "GET", url: `${nativeBase}/props` };
  if (preset === "ollama-running" || preset === "ollama-show") {
    const apiBase = nativeBase.endsWith("/api") ? nativeBase : `${nativeBase}/api`;
    if (preset === "ollama-running") return { method: "GET", url: `${apiBase}/ps` };
    const selected = model.trim();
    if (!selected) throw new Error("Choose an Ollama model to query");
    return { method: "POST", url: `${apiBase}/show`, body: { model: selected } };
  }
  throw new Error("Unknown metadata request preset");
}

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null;
}
function tokens(value: unknown): number | undefined {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0 ? value : undefined;
}
function name(value: unknown): string { return typeof value === "string" ? value.trim() : ""; }

/** Parse only explicit, complete JSON metadata. A name containing '128k',
 * a successful request, or a generation default such as max_tokens proves
 * nothing about the context limit. Conflicting explicit fields stay visible
 * as separate evidence rows rather than being silently reconciled. */
export function extractModelMetadata(body: string, preset: ModelMetadataPreset): { rows: ModelMetadataRow[]; validJSON: boolean } {
  let parsed: unknown;
  try { parsed = JSON.parse(body); } catch { return { rows: [], validJSON: false }; }
  const root = record(parsed), rows: ModelMetadataRow[] = [];
  if (!root) return { rows, validJSON: true };

  if (preset === "vllm" && Array.isArray(root.data)) {
    root.data.forEach((value, index) => {
      const item = record(value); if (!item) return;
      const model = name(item.id), output = tokens(item.max_output_tokens);
      const signals = [
        ["max_model_len", "runtime"], ["context_window", "declared"], ["context_length", "declared"],
      ] as const;
      let hasContext = false;
      for (const [field, kind] of signals) {
        const context = tokens(item[field]); if (context === undefined) continue;
        hasContext = true;
        rows.push({ model, contextWindow: context, ...(output === undefined ? {} : { maxOutputTokens: output }), source: `data[${index}].${field}${output === undefined ? "" : `; data[${index}].max_output_tokens`}`, kind });
      }
      if (!hasContext && output !== undefined) rows.push({ model, maxOutputTokens: output, source: `data[${index}].max_output_tokens`, kind: "declared" });
    });
  } else if (preset === "ollama-running" && Array.isArray(root.models)) {
    root.models.forEach((value, index) => {
      const item = record(value), context = tokens(item?.context_length);
      if (!item || context === undefined) return;
      rows.push({ model: name(item.name) || name(item.model), contextWindow: context, source: `models[${index}].context_length`, kind: "runtime" });
    });
  } else if (preset === "ollama-show") {
    const model = name(root.model) || name(root.name), info = record(root.model_info);
    if (info) {
      const architecture = name(info["general.architecture"]);
      for (const [field, value] of Object.entries(info)) {
        const context = tokens(value);
        // Multimodal files may contain vision/audio context metadata as well.
        // An advertised architecture identifies which context belongs to the
        // language model; otherwise retain only top-level architecture keys.
        const modelContext = architecture ? field === `${architecture}.context_length` : /^[^.]+\.context_length$/.test(field);
        if (modelContext && context !== undefined) rows.push({ model, contextWindow: context, source: `model_info.${field}`, kind: "declared" });
      }
    }
    // /api/show serializes its Modelfile defaults as text. This is a configured
    // default, not proof that the same context is allocated by a running model.
    if (typeof root.parameters === "string") {
      const matches = [...root.parameters.matchAll(/^\s*num_ctx\s+([0-9]+)\s*$/gm)];
      matches.forEach((match, index) => {
        const context = tokens(Number(match[1]));
        if (context !== undefined) rows.push({ model, contextWindow: context, source: `parameters.num_ctx${matches.length > 1 ? `[${index}]` : ""}`, kind: "configured" });
      });
    }
  } else if (preset === "llamacpp") {
    const context = tokens(record(root.default_generation_settings)?.n_ctx);
    if (context !== undefined) rows.push({ model: name(root.model_alias) || name(root.model_path).split(/[\\/]/).pop() || "", contextWindow: context, source: "default_generation_settings.n_ctx", kind: "runtime" });
  }
  return { rows, validJSON: true };
}
