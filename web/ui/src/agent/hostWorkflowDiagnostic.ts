import { getLocale, gt } from "@/i18n";

export interface HostWorkflowDiagnostic {
  code?: string;
  message?: string;
  fix_hint?: string;
  field_path?: string;
  step_id?: string;
  node_id?: string;
}
export type HostWorkflowDiagnosticDomain = "agentWorkflow";
const codes: Record<HostWorkflowDiagnosticDomain, ReadonlySet<string>> = {
  agentWorkflow: new Set(["invalid_name", "invalid_description", "invalid_tool_approval_mode", "too_many_inputs", "invalid_input", "invalid_inputs", "invalid_step_count", "invalid_step_id", "invalid_title", "invalid_kind", "unexpected_repeat", "invalid_repeat", "unexpected_code_policy", "unexpected_code", "invalid_code", "invalid_code_step", "invalid_code_policy", "unexpected_condition", "invalid_resources", "invalid_control_step", "invalid_condition_source", "invalid_code_binding", "invalid_repeat_binding", "invalid_agent_step", "invalid_confirm_step", "invalid_instruction", "invalid_output_file", "reserved_output_path", "reserved_resource_output", "reserved_iteration_output", "invalid_template", "unknown_input", "unknown_step", "unsupported_template", "invalid_graph_version", "invalid_graph_size", "invalid_edge", "invalid_branch", "unexpected_branch", "duplicate_edge", "invalid_node_degree", "graph_cycle", "unreachable_node", "missing_condition", "invalid_condition_value", "unexpected_condition_value", "invalid_condition_number", "invalid_condition_operator"]),
};

/** Only host-owned validation codes are localized. Unknown diagnostics and all
 * original technical details remain available verbatim; no execution or
 * validation decision is derived from presentation copy. */
export function hostWorkflowDiagnostic(diagnostic: HostWorkflowDiagnostic, domain: HostWorkflowDiagnosticDomain) {
  const originalMessage = typeof diagnostic.message === "string" ? diagnostic.message : "";
  const known = !!diagnostic.code && codes[domain].has(diagnostic.code);
  const message = known && getLocale() !== "zh-CN"
    ? gt(`workflowDiagnostics.${domain}.${diagnostic.code}`) : originalMessage;
  const hint = diagnostic.fix_hint || "";
  const changed = message !== originalMessage || hint !== (diagnostic.fix_hint || "");
  return {
    message, hint,
    location: [diagnostic.step_id || diagnostic.node_id, diagnostic.field_path].filter(Boolean).join(" · "),
    code: diagnostic.code || "",
    original: changed ? [originalMessage, diagnostic.fix_hint].filter(Boolean).join("\n") : "",
  };
}
