import type { Starters } from "@/api/types";

// The file behind 下载样例: a complete, ready-to-import 20-card set for the
// malware-analysis deployment — 10 medium openings first, then 10 heavy ones,
// with three cards driving the docked web apps through /skill:browser-use.
//
// Every card is self-contained: a first message may not lean on a previous
// turn ("对上面那个样本…"), because on the empty screen there is no previous
// turn — cards that reference one belong in followups, not here. Prompts stay
// inside the server's limits (title ≤ 40, label ≤ 30, prompt ≤ 2000) and use
// only icon names the closed set knows, so the file round-trips through
// 「导入 JSON」 without a warning.
export const starterSample: Starters = {
  heading: "今天要分析什么？",
  send: false,
  cards: [
    // ── 中等（10）：单步或短链路，先热身 ──────────────────────────────────
    {
      icon: "graph",
      title: "让 agent 操作样本库页面",
      label: "/skill:browser-use",
      prompt:
        "/skill:browser-use 打开样本库页面（面板里的样本库），在检索页把「最大检出数」筛成 0，浏览结果列表并告诉我前 5 条的家族、类型和首见日期；最后说明页面上哪些数据带「演示数据」标注。",
    },
    {
      icon: "search",
      title: "找一个杀软未检出的样本",
      label: "零检出 + 有二进制",
      prompt:
        "在样本库里找一个 detections 为 0 且有二进制文件的样本。查看它的详情和 CAPA 标签，判断它是否具有恶意行为，给出结论、逐条证据和置信度。不要仅凭检出数为 0 就下结论。",
    },
    {
      icon: "code",
      title: "反编译 dropper 看函数意图",
      label: "Ghidra 伪 C",
      prompt:
        "对样本库里的 toy_dropper 做 Ghidra 反编译，分析关键函数的功能意图。只做静态分析，绝不执行样本。说明哪些结论直接来自伪 C 代码，哪些属于推测。",
    },
    {
      icon: "wand",
      title: "为 stealer 生成 Yara 规则",
      label: "yarGen + 人工复核",
      prompt:
        "对样本库里的 toy_stealer 生成一条 Yara 规则，解释其中最有区分度的字符串，并指出哪些通用字符串应当人工删除。不要把自动生成的规则说成已经过生产验证。",
    },
    {
      icon: "book",
      title: "解读一个信息丰富的样本",
      label: "标签 + 沙箱日志 + 摘要",
      prompt:
        "找一个 Qakbot 样本，查看它的标签、压缩后的动态沙箱日志和 LLM 摘要，按「主要行为、持久化、网络活动、风险」四部分向我解释。区分数据库里的事实与模型推断。",
    },
    {
      icon: "spark",
      title: "查看家族画像与影响面",
      label: "注意标注演示数据",
      prompt:
        "介绍 Qakbot 家族：库内样本数量、类型构成、高频行为标签、活动时间跨度和百科介绍，再给出传播趋势、地域分布和攻击基础设施。明确说明每一部分的数据来源，合成的演示数据必须标注出来。",
    },
    {
      icon: "fingerprint",
      title: "相似样本与变体判断",
      label: "ssdeep + TLSH",
      prompt:
        "对 toy_dropper 查询相似样本，分别解释 ssdeep 分数与 tlsh_diff 的口径，结合文件类型和家族字段判断哪些可能是同一变体。如果库里相似样本很少，如实说明结论范围有限。",
    },
    {
      icon: "database",
      title: "样本库统计概览",
      label: "家族 / 类型 / 检出分布",
      prompt:
        "给我一份样本库的整体概览：总样本数、家族分布、文件类型构成、检出数分布，以及多少样本带二进制、多少带沙箱日志。用 malquery 完成查询，最后用几张小表呈现，并指出库里数据量最大的三个家族。",
    },
    {
      icon: "target",
      title: "用浏览器查 ATT&CK 战术",
      label: "/skill:browser-use",
      prompt:
        "/skill:browser-use 打开 ATT&CK 百科页面（面板里的百科），浏览「持久化」战术下的技术列表，挑出 3 个与凭据窃取或窃密相关的技术；再结合样本库里 stealer 类样本的 CAPA 标签，说明哪些行为能对上这些技术。区分百科内容与库内证据。",
    },
    {
      icon: "globe",
      title: "用浏览器查家族情报",
      label: "/skill:browser-use",
      prompt:
        "/skill:browser-use 打开情报页面（面板里的情报），搜索 Qakbot 相关情报，汇总它的活动趋势、传播渠道和基础设施数据，最后明确标注哪些字段是演示数据、哪些来自情报库。",
    },

    // ── 复杂（10）：多阶段 + 计划跟踪 + 子代理并行，参考测试问题示例-complex.md ──
    {
      icon: "layers",
      title: "四样本静态分析流水线",
      label: "并行 + 汇总对比",
      prompt:
        "对样本库里全部 4 个带二进制的样本，各自走完整套静态分析：算 5 种哈希、跑 CAPA 行为标签、Ghidra 反编译关键函数、生成 Yara 规则、查相似样本。多个样本的分析并行做，不要串行。最后汇总成一张横向对比表，判断哪两个最可能是同一家族的变体、哪个最像良性，每个判断都带证据和置信度。",
    },
    {
      icon: "users",
      title: "全家族横向画像对比",
      label: "子代理并行取数",
      prompt:
        "盘点样本库里所有家族，为每个家族生成结构化画像：库内样本数、主要文件类型、高频行为标签、活动时间跨度、百科要点。每个家族的画像用子代理并行去取，主流程不要串行挨个查。最后做一张横向对比表，指出哪个家族攻击面最广、哪个最隐蔽，并明确区分「数据库事实」与「LLM 生成的百科」。",
    },
    {
      icon: "scan",
      title: "零检出样本复审报告",
      label: "计划跟踪 + 置信度",
      prompt:
        "扫描库里所有 max-detections=0 且有二进制的样本，对每一个查详情、跑 CAPA、反编译关键函数，给出恶意性判定（结论 + 逐条证据 + 置信度）。先制定计划并逐项跟踪进度，分阶段执行。最后产出一份《零检出样本复审报告》，按置信度排序，并给出是否建议加入检测规则的结论。整个过程不执行任何样本。",
    },
    {
      icon: "share",
      title: "相似样本二级聚簇",
      label: "二级扩展 + 关系网",
      prompt:
        "选一个带二进制的 dropper 样本，先查它的相似样本；再对每个相似样本各跑一次相似查询做二级扩展，把所有关系汇成一个聚簇，判断哪些是同一变体。二级扩展用子代理并行。最后解释 ssdeep 越高越像、TLSH diff 越低越像的口径，并提醒库里只有 4 个二进制、结论范围有限。",
    },
    {
      icon: "eye",
      title: "数据真实性审计",
      label: "区分事实与合成数据",
      prompt:
        "做一次数据真实性审计，分阶段执行并跟踪进度：1) 遍历所有家族，把每个画像里「数据库事实」和「LLM 生成 / 合成演示数据」分开标注，找出可能被误当成真实证据的合成字段（尤其 trend / geo / infra）；2) 挑一个只有 CAPE 日志、没有二进制的样本，验证不会对它调用 Ghidra / Yara；3) 产出审计清单，列出每个发现和风险等级。",
    },
    {
      icon: "network",
      title: "行为杀伤链流程图",
      label: "Mermaid + 证据标注",
      prompt:
        "选一个带二进制的 dropper 样本做完整静态分析（CAPA 行为标签 + Ghidra 反编译关键函数，若有 CAPE 日志一并看），然后用 Mermaid 流程图画出行为杀伤链：入口/触发 → 持久化 → 环境探测 → 网络/C2 → 载荷释放与执行。每个节点注明证据来源（CAPA 标签名 / 伪 C 代码 / 日志），推测的步骤用虚线或显式标注「推测」。先制定计划并逐项跟踪，分析阶段可用子代理并行。不要执行样本。",
    },
    {
      icon: "clock",
      title: "家族时间线与关系网",
      label: "甘特图 + 关系图",
      prompt:
        "盘点 Qakbot 家族，用两张 Mermaid 图呈现：① 甘特图画它的活动时间跨度，锚定 first_seen 等数据库事实；② 关系图画出库内样本之间的相似关系（ssdeep / TLSH 作为边）。两张图都明确标注哪些是数据库事实、哪些是合成演示数据。时间线与相似关系数据用子代理并行去取。",
    },
    {
      icon: "table",
      title: "ATT&CK 能力覆盖矩阵",
      label: "CAPA → 战术映射",
      prompt:
        "盘点库内所有带 CAPA 结果的样本，把行为能力映射到 ATT&CK 战术阶段（初始访问、执行、持久化、防御规避、凭据窃取、发现、命令控制、渗出），产出一张「家族 × 战术阶段」覆盖矩阵。用子代理并行整理各家族的能力，主流程汇总。矩阵每格注明证据来源（CAPA 标签），没覆盖到的格子不要臆造，最后指出检测盲区在哪些阶段。",
    },
    {
      icon: "compare",
      title: "Dropper 变体进化审计",
      label: "v1 对比 v2",
      prompt:
        "对库里的 toy_dropper 与 toy_dropper_v2 做一次逐项差异审计：5 种哈希、CAPA 能力标签、Ghidra 反编译关键函数、可读字符串。判断 v2 相对 v1 具体改了什么（新增能力？C2 地址变化？规避手段？），输出一份「变体进化报告」，每个结论标注是代码证据还是推测。不要执行样本。",
    },
    {
      icon: "shield",
      title: "家族级 Yara 规则集",
      label: "生成 + 交叉验证",
      prompt:
        "为样本库里的主要家族各生成一条 Yara 规则（优先有二进制的家族），然后用库内其他家族的样本做交叉测试，检查规则之间是否互相误报。先制定计划并逐项跟踪。产出规则集和每条规则的覆盖说明，并提醒自动生成的规则需人工复核后才能投产。",
    },
  ],
};
