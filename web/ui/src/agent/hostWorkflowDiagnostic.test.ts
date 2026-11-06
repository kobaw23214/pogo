import { readFileSync } from "node:fs";
import { afterEach, describe, expect, it } from "vitest";
import { computed } from "vue";
import { messageCatalogs, setLocale, type MessageTree } from "@/i18n";
import { hostWorkflowDiagnostic } from "./hostWorkflowDiagnostic";

afterEach(() => setLocale("zh-CN"));

function catalogText(locale: "zh-CN" | "en", path: string[]): string {
  let value: string | MessageTree = messageCatalogs[locale];
  for (const key of path) {
    if (typeof value !== "object") throw new Error(`Missing catalog namespace: ${locale}.${path.join(".")}`);
    value = value[key];
  }
  if (typeof value !== "string" || !value.trim()) throw new Error(`Missing catalog message: ${locale}.${path.join(".")}`);
  return value;
}

function diagnosticCatalog(locale: "zh-CN" | "en"): Record<string, string> {
  const root = messageCatalogs[locale].workflowDiagnostics;
  if (!root || typeof root !== "object") throw new Error(`Missing diagnostic catalog: ${locale}`);
  const result: Record<string, string> = {};
  function visit(tree: MessageTree, prefix = "") {
    for (const [key, value] of Object.entries(tree)) {
      if (typeof value === "string") result[prefix + key] = value;
      else visit(value, `${prefix}${key}.`);
    }
  }
  visit(root);
  return result;
}

describe("host workflow diagnostic localization", () => {
  it("ships matching Chinese and English diagnostic keys and interpolation arguments in the actual catalog", () => {
    const chinese = diagnosticCatalog("zh-CN"), english = diagnosticCatalog("en");
    expect(Object.keys(english).sort()).toEqual(Object.keys(chinese).sort());
    const parameters = (message: string) => [...message.matchAll(/\{([A-Za-z_]\w*)\}/g)].map(match => match[1]).sort();
    for (const [key, message] of Object.entries(english)) {
      expect(message.trim(), key).toBeTruthy();
      expect(message, key).not.toMatch(/\p{Script=Han}/u);
      expect(parameters(message), key).toEqual(parameters(chinese[key]));
    }
  });

  it("localizes known host codes while keeping the complete original diagnosis", () => {
    setLocale("en");
    const diagnostic = Object.freeze({ code: "invalid_input", message: "输入选项重复：用户定义值", field_path: "inputs[2]", step_id: "step-custom" });
    const row = hostWorkflowDiagnostic(diagnostic, "agentWorkflow");
    expect(row.message).toBe(catalogText("en", ["workflowDiagnostics", "agentWorkflow", "invalid_input"]));
    expect(row.message).not.toBe("workflowDiagnostics.agentWorkflow.invalid_input");
    expect(row.original).toBe(diagnostic.message);
    expect(row.location).toBe("step-custom · inputs[2]");
    expect(diagnostic.message).toBe("输入选项重复：用户定义值");
  });


  it("keeps host hints and unknown diagnostics verbatim", () => {
    setLocale("en");
    const known = { code: "invalid_agent_step", message: "Agent 节点缺少任务说明", fix_hint: "填写本节点 task" };
    expect(hostWorkflowDiagnostic(known, "agentWorkflow").hint).toBe(known.fix_hint);
    expect(hostWorkflowDiagnostic(known, "agentWorkflow").original).toBe(`${known.message}\n${known.fix_hint}`);
    const external = { code: "future_or_external_code", message: "用户与远端返回的原始内容", fix_hint: "填写本节点 task" };
    const unknown = hostWorkflowDiagnostic(external, "agentWorkflow");
    expect(unknown.message).toBe(external.message);
    expect(unknown.hint).toBe(external.fix_hint);
    expect(unknown.original).toBe("");
    expect(hostWorkflowDiagnostic({ ...known, fix_hint: "用户自定义的修复建议" }, "agentWorkflow").hint).toBe("用户自定义的修复建议");
  });

  it("reacts to locale changes without changing the diagnostic or validation decision", () => {
    const diagnostic = { code: "graph_cycle", message: "图流程不允许循环", severity: "error" };
    setLocale("zh-CN");
    const row = computed(() => hostWorkflowDiagnostic(diagnostic, "agentWorkflow"));
    expect(row.value.message).toBe(diagnostic.message);
    expect(row.value.original).toBe("");
    setLocale("en");
    expect(row.value.message).toBe("Cycles are not allowed in graph workflows.");
    expect(row.value.original).toBe(diagnostic.message);
    expect(diagnostic.severity).toBe("error");
  });

  it("covers every diagnostic code currently emitted by the internal host validators", () => {
    setLocale("en");
    const files = {
      agentWorkflow: ["agentworkflow/definition.go", "agentworkflow/graph.go"],
    } as const;
    for (const domain of ["agentWorkflow"] as const) {
      for (const file of files[domain]) {
        const source = readFileSync(new URL(`../../../../${file}`, import.meta.url), "utf8");
        for (const match of source.matchAll(/add\("([a-z_]+)"/g)) {
          const message = hostWorkflowDiagnostic({ code: match[1], message: "ORIGINAL_HOST_DETAIL" }, domain).message;
          expect(message, `${domain}.${match[1]}`).toBe(catalogText("en", ["workflowDiagnostics", domain, match[1]]));
          expect(message, `${domain}.${match[1]}`).not.toBe("ORIGINAL_HOST_DETAIL");
        }
      }
    }
  });
});
