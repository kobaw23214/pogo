import { describe, expect, it } from "vitest";
import { extractModelMetadata, modelMetadataRequest, type ModelMetadataPreset } from "./modelMetadata";

describe("read-only model metadata request mapping", () => {
  it("preserves nested gateway prefixes and never changes the selected origin", () => {
    const base = " https://gateway.example:8443/team/compatible-mode/v1/// ";
    expect(modelMetadataRequest(base, "unused", "vllm")).toEqual({ method: "GET", url: "https://gateway.example:8443/team/compatible-mode/v1/models" });
    expect(modelMetadataRequest(base, "unused", "llamacpp")).toEqual({ method: "GET", url: "https://gateway.example:8443/team/compatible-mode/props" });
    expect(modelMetadataRequest(base, "unused", "ollama-running")).toEqual({ method: "GET", url: "https://gateway.example:8443/team/compatible-mode/api/ps" });
  });

  it.each(["http://localhost:11434", "http://localhost:11434/v1", "http://localhost:11434/api", "http://localhost:11434/api/v1/"])("queries Ollama metadata from %s without duplicating /api", base => {
    expect(modelMetadataRequest(base, " gemma4:latest ", "ollama-show")).toEqual({ method: "POST", url: "http://localhost:11434/api/show", body: { model: "gemma4:latest" } });
    expect(modelMetadataRequest(base, "", "ollama-running")).toEqual({ method: "GET", url: "http://localhost:11434/api/ps" });
  });

  it("uses fixed read-only routes, without inference bodies or automatic model loading", () => {
    expect(modelMetadataRequest("http://[::1]:8080/v1", "ignored", "llamacpp")).toEqual({ method: "GET", url: "http://[::1]:8080/props" });
    expect(modelMetadataRequest("http://localhost/v10", "ignored", "llamacpp").url).toBe("http://localhost/v10/props");
    expect(modelMetadataRequest("http://localhost/V1", "ignored", "llamacpp").url).toBe("http://localhost/V1/props");
    expect(() => modelMetadataRequest("http://localhost/v1", "   ", "ollama-show")).toThrow();
    expect(() => modelMetadataRequest("http://localhost/v1", "model", "other" as ModelMetadataPreset)).toThrow();
  });

  it.each([
    "", "/v1", "file:///tmp/model", "ftp://host/v1", "http:///", "http:///host/v1", "http://user:pass@host/v1", "http://@host/v1", "http://host/v1?", "http://host/v1?key=value", "http://host/v1#", "http://host/v1#fragment", "http://host/a b/v1", "http://host/a\nb/v1", "http://host/a\u00a0b/v1", "http://host\\evil/v1", "http://host:99999/v1",
  ])("rejects an ambiguous or unsafe Base URL without inventing another endpoint: %s", base => {
    expect(() => modelMetadataRequest(base, "model", "vllm")).toThrow();
  });
});

describe("model metadata evidence extraction", () => {
  it("displays explicit runtime vLLM context and declared output metadata without changing source JSON", () => {
    const raw = JSON.stringify({ data: [{ id: "served-alias", max_model_len: 32768, max_output_tokens: 8192 }] });
    expect(extractModelMetadata(raw, "vllm")).toEqual({ validJSON: true, rows: [{ model: "served-alias", contextWindow: 32768, maxOutputTokens: 8192, source: "data[0].max_model_len; data[0].max_output_tokens", kind: "runtime" }] });
    expect(JSON.parse(raw)).toEqual({ data: [{ id: "served-alias", max_model_len: 32768, max_output_tokens: 8192 }] });
  });

  it("keeps generic declarations and conflicting context fields visible instead of silently selecting a value", () => {
    const result = extractModelMetadata(JSON.stringify({ data: [{ id: "model", max_model_len: 32768, context_window: 131072 }, { id: "second", context_length: 65536 }, { id: "output-only", max_output_tokens: 4096 }] }), "vllm");
    expect(result.rows.map(row => [row.model, row.contextWindow, row.maxOutputTokens, row.kind])).toEqual([
      ["model", 32768, undefined, "runtime"], ["model", 131072, undefined, "declared"], ["second", 65536, undefined, "declared"], ["output-only", undefined, 4096, "declared"],
    ]);
  });

  it("does not infer limits from names, successful JSON, request defaults or non-integer metadata", () => {
    for (const value of [null, 0, -1, 1.5, "131072", Number.MAX_SAFE_INTEGER + 1, true, {}]) {
      const result = extractModelMetadata(JSON.stringify({ data: [{ id: "model-128k", max_model_len: value, context_window: value, context_length: value, max_output_tokens: value, max_tokens: 8192 }] }), "vllm");
      expect(result).toEqual({ validJSON: true, rows: [] });
    }
  });

  it("extracts Ollama allocated context from running models rather than their model names", () => {
    expect(extractModelMetadata(JSON.stringify({ models: [{ name: "gemma4", context_length: 4096 }, { model: "qwen:latest", context_length: 32768 }, { name: "nominal-128k" }, null] }), "ollama-running")).toEqual({ validJSON: true, rows: [
      { model: "gemma4", contextWindow: 4096, source: "models[0].context_length", kind: "runtime" },
      { model: "qwen:latest", contextWindow: 32768, source: "models[1].context_length", kind: "runtime" },
    ] });
  });

  it("distinguishes Ollama model declarations from configured defaults without claiming either is allocated", () => {
    const result = extractModelMetadata(JSON.stringify({ name: "gemma4", model_info: { "gemma4.context_length": 131072, "gemma4.embedding_length": 2560, "other.context_length": "8192" }, parameters: "temperature 0.7\n num_ctx 2048\nnum_predict 4096\n" }), "ollama-show");
    expect(result).toEqual({ validJSON: true, rows: [
      { model: "gemma4", contextWindow: 131072, source: "model_info.gemma4.context_length", kind: "declared" },
      { model: "gemma4", contextWindow: 2048, source: "parameters.num_ctx", kind: "configured" },
    ] });
  });

  it("does not accept invalid or embedded Ollama parameter descriptions as num_ctx settings", () => {
    const result = extractModelMetadata(JSON.stringify({ parameters: "# num_ctx 131072\ntemplate num_ctx 8192\nnum_ctx -1\nnum_ctx 0\nnum_ctx 9007199254740992\nnum_ctx 1.5\nnum_ctx 4096 extra\nnum_ctx 8192\nnum_ctx 16384" }), "ollama-show");
    expect(result.rows.map(row => [row.contextWindow, row.kind])).toEqual([[8192, "configured"], [16384, "configured"]]);
  });

  it("uses the advertised language architecture instead of a vision or audio context length", () => {
    const result = extractModelMetadata(JSON.stringify({ model_info: { "general.architecture": "gemma4", "gemma4.context_length": 131072, "clip.context_length": 77, "gemma4.vision.context_length": 512, "gemma4.audio.context_length": 2048 } }), "ollama-show");
    expect(result.rows).toEqual([{ model: "", contextWindow: 131072, source: "model_info.gemma4.context_length", kind: "declared" }]);
    expect(extractModelMetadata('{"model_info":{"gemma4.vision.context_length":512}}', "ollama-show").rows).toEqual([]);
  });

  it("reads llama.cpp's active context but never mistakes generation defaults for maximum output capacity", () => {
    expect(extractModelMetadata(JSON.stringify({ model_path: "/private/models/model.gguf", default_generation_settings: { n_ctx: 16384, params: { n_predict: 4096, max_tokens: 4096 } } }), "llamacpp")).toEqual({ validJSON: true, rows: [
      { model: "model.gguf", contextWindow: 16384, source: "default_generation_settings.n_ctx", kind: "runtime" },
    ] });
    expect(extractModelMetadata('{"default_generation_settings":{"n_ctx":0}}', "llamacpp").rows).toEqual([]);
  });

  it("distinguishes malformed responses from valid JSON without recognizable metadata", () => {
    for (const raw of ["", "<html>upstream error</html>", '{"data":[']) expect(extractModelMetadata(raw, "vllm")).toEqual({ validJSON: false, rows: [] });
    for (const raw of ["null", "[]", "true", '"131072"', "{}", '{"error":"not supported"}', '{"data":[null,1,{},[]]}']) expect(extractModelMetadata(raw, "vllm")).toEqual({ validJSON: true, rows: [] });
  });
});
