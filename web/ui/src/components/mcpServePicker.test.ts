import { describe, expect, it } from "vitest";
import type { MCPServeCatalog } from "@/api/types";
import {
  serveChecksFromInclude,
  serveIncludeFromChecks,
  servePickerModel,
  toolPatternMatches,
} from "./mcpServePicker";

// The fixture covers one group of each shape: the builtin agent_* family,
// two dotted skill families plus a dotless one (and an unregistered skill
// that must never show), a connected server with a read-only and a write
// tool, and a disconnected server with no tools at all.
const catalog: MCPServeCatalog = {
  builtin: [
    { name: "agent_submit_task", title: "提交任务" },
    { name: "agent_task_status", title: "任务状态" },
    { name: "agent_task_wait", title: "等待任务" },
  ],
  skills: [
    { name: "mal.search", title: "检索", registered: true },
    { name: "mal.fetch", title: "抓取", registered: true },
    { name: "ti.lookup", title: "情报查询", registered: true },
    { name: "mal.draft", title: "未注册", registered: false },
    { name: "solo", title: "无点号", registered: true },
  ],
  servers: [
    {
      name: "filesystem",
      state: "connected",
      tools: [
        { name: "filesystem.read_file", remote: "read_file", title: "Read file", description: "Read a file", read_only: true },
        { name: "filesystem.write_file", remote: "write_file", title: "Write file", description: "Write a file", read_only: false },
      ],
    },
    { name: "offline", state: "disconnected", tools: [] },
  ],
};

function checked(...names: string[]): Set<string> {
  return new Set(names);
}

describe("toolPatternMatches", () => {
  it("matches exact names and trailing-* prefixes, nothing else", () => {
    expect(toolPatternMatches("mal.search", "mal.search")).toBe(true);
    expect(toolPatternMatches("mal.search", "mal.fetch")).toBe(false);
    expect(toolPatternMatches("agent_*", "agent_submit_task")).toBe(true);
    expect(toolPatternMatches("mal.*", "mal.search")).toBe(true);
    // The wildcard is a prefix over "mal." — the bare family name escapes it.
    expect(toolPatternMatches("mal.*", "mal")).toBe(false);
    expect(toolPatternMatches("agent_*", "filesystem.read_file")).toBe(false);
  });
});

describe("servePickerModel", () => {
  it("groups in display order: builtin, skill families, servers", () => {
    const model = servePickerModel(catalog);
    expect(model.map((g) => g.id)).toEqual(["builtin", "skill:mal", "skill:ti", "skill:solo", "server:filesystem", "server:offline"]);
    expect(model.map((g) => g.wildcard)).toEqual(["agent_*", "mal.*", "ti.*", null, "filesystem.*", "offline.*"]);
  });

  it("drops unregistered skills and keeps a tool-less server group (greyed by state)", () => {
    const model = servePickerModel(catalog);
    const skills = model.filter((g) => g.kind === "skill").flatMap((g) => g.tools.map((t) => t.name));
    expect(skills).not.toContain("mal.draft");
    const offline = model.find((g) => g.id === "server:offline");
    expect(offline).toMatchObject({ kind: "forward", serverName: "offline", serverState: "disconnected", tools: [] });
  });

  it("tolerates a missing or partial catalog", () => {
    expect(servePickerModel(undefined)).toEqual([]);
    expect(servePickerModel(null)).toEqual([]);
    expect(servePickerModel({ builtin: [], skills: [], servers: [] })).toEqual([]);
    expect(servePickerModel({ builtin: [{ name: "agent_submit_task", title: "t" }] } as MCPServeCatalog).map((g) => g.id)).toEqual(["builtin"]);
  });
});

describe("serveChecksFromInclude", () => {
  it("an empty include checks every builtin + skill, never forwards (the expose-all default)", () => {
    const out = serveChecksFromInclude([], catalog);
    expect(out).toEqual(
      checked("agent_submit_task", "agent_task_status", "agent_task_wait", "mal.search", "mal.fetch", "ti.lookup", "solo"),
    );
  });

  it("checks exact-name hits only", () => {
    expect(serveChecksFromInclude(["mal.search", "filesystem.read_file"], catalog)).toEqual(
      checked("mal.search", "filesystem.read_file"),
    );
  });

  it("checks whatever a wildcard covers, forwards included", () => {
    expect(serveChecksFromInclude(["agent_*"], catalog)).toEqual(
      checked("agent_submit_task", "agent_task_status", "agent_task_wait"),
    );
    expect(serveChecksFromInclude(["mal.*"], catalog)).toEqual(checked("mal.search", "mal.fetch"));
    expect(serveChecksFromInclude(["filesystem.*"], catalog)).toEqual(
      checked("filesystem.read_file", "filesystem.write_file"),
    );
  });

  it("patterns that hit nothing check nothing — including the '_' sentinel", () => {
    expect(serveChecksFromInclude(["gone.*", "nope"], catalog)).toEqual(new Set());
    expect(serveChecksFromInclude(["_"], catalog)).toEqual(new Set());
  });

  it("is robust without a catalog", () => {
    expect(serveChecksFromInclude([], undefined)).toEqual(new Set());
    expect(serveChecksFromInclude(["agent_*"], null)).toEqual(new Set());
  });
});

describe("serveIncludeFromChecks", () => {
  it("collapses a fully checked group to its wildcard", () => {
    expect(serveIncludeFromChecks(checked("agent_submit_task", "agent_task_status", "agent_task_wait"), catalog)).toEqual(["agent_*"]);
    expect(serveIncludeFromChecks(checked("mal.search", "mal.fetch"), catalog)).toEqual(["mal.*"]);
    expect(serveIncludeFromChecks(checked("filesystem.read_file", "filesystem.write_file"), catalog)).toEqual(["filesystem.*"]);
  });

  it("lists a partially checked group by exact names", () => {
    expect(serveIncludeFromChecks(checked("mal.search"), catalog)).toEqual(["mal.search"]);
    expect(serveIncludeFromChecks(checked("filesystem.write_file"), catalog)).toEqual(["filesystem.write_file"]);
    // The dotless skill has no prefix to wildcard over: always exact.
    expect(serveIncludeFromChecks(checked("solo"), catalog)).toEqual(["solo"]);
  });

  it("renders mixed groups in display order (builtin, skills, servers)", () => {
    const out = serveIncludeFromChecks(
      checked("filesystem.read_file", "mal.search", "mal.fetch", "agent_task_wait", "agent_submit_task", "agent_task_status", "solo"),
      catalog,
    );
    expect(out).toEqual(["agent_*", "mal.*", "solo", "filesystem.read_file"]);
  });

  it("drops checked names the catalog no longer lists", () => {
    expect(serveIncludeFromChecks(checked("gone.tool", "mal.search"), catalog)).toEqual(["mal.search"]);
  });

  it("emits the '_' sentinel when nothing is checked — never [] (which would expose all)", () => {
    expect(serveIncludeFromChecks(new Set(), catalog)).toEqual(["_"]);
    expect(serveIncludeFromChecks(checked("gone.tool"), catalog)).toEqual(["_"]);
    expect(serveIncludeFromChecks(new Set(), undefined)).toEqual(["_"]);
  });

  it("round-trips through serveChecksFromInclude", () => {
    for (const include of [["agent_*", "mal.*"], ["filesystem.read_file"], ["agent_task_wait", "solo"], ["_"]]) {
      expect(serveIncludeFromChecks(serveChecksFromInclude(include, catalog), catalog)).toEqual(include);
    }
    // The expose-all default projects to the explicit equivalent.
    expect(serveIncludeFromChecks(serveChecksFromInclude([], catalog), catalog)).toEqual(["agent_*", "mal.*", "ti.*", "solo"]);
  });
});

// Runtime skill packages: installed bundles surface as one opt-in group per
// package. The fixture carries the two builtin skill_* helper rows that a
// runtime-enabled catalog adds (they must not ride the agent_* wildcard), a
// package whose tools share a word-boundary prefix (skill_malware_*), one
// whose LCP stops mid-word (skill_pe — no wildcard), and a tool-less revoked
// package.
const catalogRuntime: MCPServeCatalog = {
  builtin: [
    { name: "agent_submit_task", title: "提交任务" },
    { name: "agent_task_status", title: "任务状态" },
    { name: "agent_task_wait", title: "等待任务" },
    { name: "skill_upload_file", title: "上传样本文件" },
    { name: "skill_read_artifact", title: "读取产物" },
  ],
  skills: [{ name: "mal.search", title: "检索", registered: true }],
  servers: [],
  runtime: [
    {
      id: "rs_malware",
      name: "malware-analysis",
      package_name: "malware-analysis",
      version: "1.2.0",
      state: "available",
      tools: [
        { name: "skill_malware_sample_search", contract: "malware.sample.search", title: "样本检索", description: "按特征检索样本", read_only: true },
        {
          name: "skill_malware_detonate",
          contract: "malware.detonate",
          title: "引爆样本",
          description: "沙箱引爆并回传报告",
          read_only: false,
          file_inputs: true,
          writes_artifacts: true,
        },
      ],
    },
    {
      id: "rs_pe",
      name: "pe-tools",
      package_name: "pe-tools",
      version: "0.3.1",
      state: "needs_configuration",
      tools: [
        { name: "skill_peid_scan", contract: "peid.scan", read_only: true },
        { name: "skill_pestring_dump", contract: "pestring.dump", read_only: true },
      ],
    },
    { id: "rs_empty", name: "empty-pack", package_name: "empty-pack", version: "0.0.1", state: "revoked", tools: [] },
  ],
};

describe("servePickerModel — runtime packages", () => {
  it("groups one per package, after builtin/skills/servers, with identity + state passthrough", () => {
    const model = servePickerModel(catalogRuntime);
    expect(model.map((g) => g.id)).toEqual(["builtin", "skill:mal", "runtime:rs_malware", "runtime:rs_pe", "runtime:rs_empty"]);
    const rt = model.find((g) => g.id === "runtime:rs_malware");
    expect(rt).toMatchObject({ kind: "runtime", serverState: "available", packageName: "malware-analysis", version: "1.2.0" });
    expect(model.find((g) => g.id === "runtime:rs_pe")?.serverState).toBe("needs_configuration");
  });

  it("wildcards only a word-boundary LCP — skill_malware_* yes, mid-word skill_pe no", () => {
    const model = servePickerModel(catalogRuntime);
    expect(model.find((g) => g.id === "runtime:rs_malware")?.wildcard).toBe("skill_malware_*");
    // LCP("skill_peid_scan", "skill_pestring_dump") = "skill_pe": emitting
    // "skill_pe*" would also catch another package's skill_pe* tools.
    expect(model.find((g) => g.id === "runtime:rs_pe")?.wildcard).toBeNull();
    expect(model.find((g) => g.id === "runtime:rs_empty")?.wildcard).toBeNull();
  });

  it("carries the per-tool badge metadata (contract, read_only, file/artifact flags)", () => {
    const rt = servePickerModel(catalogRuntime).find((g) => g.id === "runtime:rs_malware");
    expect(rt?.tools[0]).toMatchObject({
      name: "skill_malware_sample_search",
      contract: "malware.sample.search",
      readOnly: true,
      fileInputs: false,
      writesArtifacts: false,
    });
    expect(rt?.tools[1]).toMatchObject({ contract: "malware.detonate", readOnly: false, fileInputs: true, writesArtifacts: true });
  });
});

describe("serveChecksFromInclude — runtime is opt-in", () => {
  it("an empty include checks builtin (helpers included) + skills, never runtime rows", () => {
    expect(serveChecksFromInclude([], catalogRuntime)).toEqual(
      checked("agent_submit_task", "agent_task_status", "agent_task_wait", "skill_upload_file", "skill_read_artifact", "mal.search"),
    );
  });

  it("checks runtime rows only on an explicit hit, wildcards included", () => {
    expect(serveChecksFromInclude(["skill_malware_*"], catalogRuntime)).toEqual(
      checked("skill_malware_sample_search", "skill_malware_detonate"),
    );
    expect(serveChecksFromInclude(["skill_peid_scan"], catalogRuntime)).toEqual(checked("skill_peid_scan"));
  });
});

describe("serveIncludeFromChecks — runtime + builtin helpers", () => {
  it("splits a fully checked builtin into agent_* + the exact helper names (agent_* must not impersonate them)", () => {
    const all = checked("agent_submit_task", "agent_task_status", "agent_task_wait", "skill_upload_file", "skill_read_artifact");
    expect(serveIncludeFromChecks(all, catalogRuntime)).toEqual(["agent_*", "skill_upload_file", "skill_read_artifact"]);
  });

  it("still emits agent_* when only the agent rows are checked, and exact helpers when only they are", () => {
    expect(serveIncludeFromChecks(checked("agent_submit_task", "agent_task_status", "agent_task_wait"), catalogRuntime)).toEqual([
      "agent_*",
    ]);
    expect(serveIncludeFromChecks(checked("skill_upload_file", "skill_read_artifact"), catalogRuntime)).toEqual([
      "skill_upload_file",
      "skill_read_artifact",
    ]);
    // A partial agent subset plus helpers: no wildcard at all.
    expect(serveIncludeFromChecks(checked("agent_task_wait", "skill_upload_file"), catalogRuntime)).toEqual([
      "agent_task_wait",
      "skill_upload_file",
    ]);
  });

  it("collapses a fully checked package to its LCP wildcard, else exact names", () => {
    expect(serveIncludeFromChecks(checked("skill_malware_sample_search", "skill_malware_detonate"), catalogRuntime)).toEqual([
      "skill_malware_*",
    ]);
    expect(serveIncludeFromChecks(checked("skill_peid_scan", "skill_pestring_dump"), catalogRuntime)).toEqual([
      "skill_peid_scan",
      "skill_pestring_dump",
    ]);
  });

  it("round-trips through serveChecksFromInclude", () => {
    for (const include of [
      ["agent_*", "skill_upload_file", "skill_read_artifact", "mal.*", "skill_malware_*"],
      ["skill_malware_detonate"],
      ["agent_*", "skill_peid_scan"],
    ]) {
      expect(serveIncludeFromChecks(serveChecksFromInclude(include, catalogRuntime), catalogRuntime)).toEqual(include);
    }
    // The expose-all default projects to the explicit equivalent — opt-in
    // runtime packages stay out of it.
    expect(serveIncludeFromChecks(serveChecksFromInclude([], catalogRuntime), catalogRuntime)).toEqual([
      "agent_*",
      "skill_upload_file",
      "skill_read_artifact",
      "mal.*",
    ]);
  });
});
