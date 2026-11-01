export default {
  namespace: "workflowApproval",
  messages: {
    "zh-CN": {
      title: "审批人分配", enable: "启用审批分配规则", legacy: "此节点保留原有审批方式。启用规则并发布新版本后，新的运行才使用这些设置。",
      preventSelf: "禁止发起人自批", preventSelfHint: "原办理人和本次发起或重试人不能处理此审批，管理员和人工授权也不例外。",
      assignment: "分配方式", fixed: "固定账号", business: "业务系统指定", approvers: "审批账号", choose: "选择审批账号", loading: "正在读取可用账号…",
      fixedHint: "运行受理时自动授予这些账号本节点的查看、审批和产物权限；发起人和不可用账号会被排除。至少需要一名合格审批人。",
      decisionRule: "任一有权审批人先处理即可结束此节点。",
      businessHint: "业务系统后端须通过接入凭据提供此节点的审批用户标识，并映射到入口中已有账号。页面输入不能指定审批人。",
      loadFailed: "审批账号读取失败：{message}", retry: "重新读取", noAccounts: "没有可用审批账号，请先配置普通账号。", unavailable: "{name}（当前不可用，保留原配置）",
      assigned: "当前审批人：{names}", unassigned: "当前没有可用审批人，请联系运行负责人或管理员重新分配。", protected: "此审批禁止发起人自批。",
      initial: "最初分配：{names}", modeFixed: "固定账号自动分配", modeBusiness: "业务系统分配",
      reason: {
        initiator_cannot_approve: "你是原办理人或本次发起人，不能审批本次运行。",
        not_assigned: "你未获分配此节点的审批权限。",
        no_eligible_approver: "当前没有合格审批人，请联系负责人重新分配。",
        account_disabled: "当前账号已停用，不能处理审批。",
      },
    },
    en: {
      title: "Approval assignment", enable: "Use approval assignment rules", legacy: "This step keeps its existing approval rules. Enable a policy and publish a new version to apply it to future runs.",
      preventSelf: "Prevent initiator self-approval", preventSelfHint: "The original owner and this run's initiator or retry initiator cannot decide this approval, including administrators or explicitly granted users.",
      assignment: "Assignment", fixed: "Fixed accounts", business: "Business system", approvers: "Approvers", choose: "Select approvers", loading: "Loading available accounts…",
      fixedHint: "Admission automatically grants these accounts access to this step, its decision and its artifacts. Initiators and unavailable accounts are excluded. At least one eligible approver is required.",
      decisionRule: "The first authorized decision settles this step.",
      businessHint: "The business backend must provide this step's approver subjects using its integration credential. They must map to accounts already configured on the publication. Browser inputs cannot select approvers.",
      loadFailed: "Could not load approvers: {message}", retry: "Load again", noAccounts: "No approver account is available. Configure ordinary accounts first.", unavailable: "{name} (unavailable; saved selection retained)",
      assigned: "Current approvers: {names}", unassigned: "No eligible approver is available. Ask the run owner or an administrator to assign one.", protected: "Initiator self-approval is prohibited for this step.",
      initial: "Initially assigned: {names}", modeFixed: "Automatically assigned accounts", modeBusiness: "Assigned by business system",
      reason: {
        initiator_cannot_approve: "You are the original owner or this run's initiator and cannot approve it.",
        not_assigned: "You are not assigned to decide this step.",
        no_eligible_approver: "No eligible approver is available. Ask the owner to assign one.",
        account_disabled: "This account is disabled and cannot decide approvals.",
      },
    },
  },
};
