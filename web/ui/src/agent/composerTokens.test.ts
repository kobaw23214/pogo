import { describe, expect, it } from "vitest";
import {
  canonicalUserSkillText,
  composerDraftWithToken,
  normalizeComposerTokenInput,
  readComposerToken,
  replaceComposerToken,
  skillMessageBlock,
} from "./composerTokens";

const skills = [{ name: "browser-use" }, { name: "docs" }, { name: "Bad--Name" }];
const picks = '<page-element source="网页" url="https://example.com/">\ntext: 中文文本\n</page-element>';
const expanded = (trailing = "", name = "browser-use") =>
  `<skill name="${name}" location="/skills/${name}/SKILL.md">\nReferences are relative to /skills/${name}.\n\n# Instructions\nDo the work.\n</skill>${trailing ? `\n\n${trailing}` : ""}`;

describe("readComposerToken", () => {
  it("captures a top skill before appending another mixed skill without losing either", () => {
    const token = readComposerToken('/skill:office 当前正文', [{name:'office'}]);
    const existing = composerDraftWithToken([{type:'text',text:'当前正文'}], token);
    const appended = [...existing, {type:'skill' as const,id:'new',name:'browser-use',label:'browser-use'}, {type:'text' as const,text:'之后网页'}];
    expect(appended.map(part => part.type === 'skill' ? part.name : part.type === 'text' ? part.text : part.type)).toEqual(['office','当前正文','browser-use','之后网页']);
  });
  it("uses compatibility aliases only after exact catalog names", () => {
    const catalog = [{name:'office',aliases:['docs']},{name:'docs'}];
    expect(readComposerToken('/skill:docs 读取', catalog)?.name).toBe('docs');
    expect(normalizeComposerTokenInput('/skill:docs 读取', catalog)).toBe('/skill:docs 读取');
    expect(readComposerToken('/skill:docs 读取', catalog.slice(0,1))?.name).toBe('office');
  });
  it("selects an exact completed skill and retains the full task", () => {
    const body = `打开 GitLab\n\n保留以下引用\n\n${picks}\n  `;
    const token = readComposerToken(`/skill:browser-use ${body}`, skills);
    expect(token).toEqual({ kind: "skill", name: "browser-use", body, prefix: "/skill:browser-use " });
    expect(replaceComposerToken(token!, "新任务\n\n下一行  ")).toBe("/skill:browser-use 新任务\n\n下一行  ");
  });

  it.each([
    "/skill:browser-use", "/skill:bro ", "/skill:browser ", "/skill:browser-use-extra task",
    "/skill:BROWSER-USE task", "/skill:unknown task", "/skill: task", "/skills:docs task",
    "read /skill:docs task", " /skill:docs task", '"/skill:docs task"', "`/skill:docs task`",
    '/skill:"docs" task', "/skill:docs\u00a0task",
  ])("does not reinterpret %j", (line) => {
    expect(readComposerToken(line, skills)).toBeNull();
    expect(normalizeComposerTokenInput(line, skills)).toBe(line);
  });

  it("waits for the available scope and uses exact server names", () => {
    expect(readComposerToken("/skill:docs task", [])).toBeNull();
    expect(normalizeComposerTokenInput("/skill:docs\ntask", [])).toBe("/skill:docs\ntask");
    expect(readComposerToken("/skill:Bad--Name task", skills)?.name).toBe("Bad--Name");
  });

  it.each([" ", "\t", "\n", "\r", "\r\n", "\f", "\v"])("normalizes only the completing separator %j", (separator) => {
    const body = ` \n\n正文\n${picks}\n `;
    const text = `/skill:docs${separator}${body}`;
    expect(readComposerToken(text, skills)?.body).toBe(body);
    expect(normalizeComposerTokenInput(text, skills)).toBe(`/skill:docs ${body}`);
    expect(normalizeComposerTokenInput(normalizeComposerTokenInput(text, skills), skills)).toBe(`/skill:docs ${body}`);
  });

  it("does not tokenize a command until completed, and never consumes arguments", () => {
    expect(readComposerToken("/compact", skills)).toBeNull();
    expect(readComposerToken("/com ", skills)).toBeNull();
    expect(readComposerToken("/compact task", skills)).toBeNull();
    expect(readComposerToken("/compact \n task", skills)).toBeNull();
    expect(readComposerToken("/compact ", skills)).toEqual({ kind: "command", name: "/compact", body: "", prefix: "/compact " });
    expect(readComposerToken("/compact \n\t", skills)?.body).toBe("\n\t");
    expect(readComposerToken("/compact ", skills, [])).toBeNull();
    expect(readComposerToken("/other ", skills, ["/other"])?.name).toBe("/other");
    expect(normalizeComposerTokenInput("/compact\n", skills)).toBe("/compact\n");
  });
});

describe("canonicalUserSkillText", () => {
  it("restores the user invocation, preserving newlines, picks, and attachment text", () => {
    const body = `分析 uploads/report.pdf\n\n${picks}\n\n\t保留最后一行  \n\n`;
    expect(canonicalUserSkillText(expanded(body))).toBe(`/skill:browser-use ${body}`);
    expect(skillMessageBlock(expanded(body))?.trailing).toBe(body);
    expect(skillMessageBlock(expanded(body))?.body).toBe("# Instructions\nDo the work.");
  });

  it("restores an invocation without task text as a completed empty-body token", () => {
    const canonical = canonicalUserSkillText(expanded());
    expect(canonical).toBe("/skill:browser-use ");
    expect(readComposerToken(canonical, skills)?.body).toBe("");
  });

  it("keeps optimistic raw text verbatim and recognizes history outside current scope", () => {
    const raw = `/skill:retired-skill  task\n${picks}\n `;
    expect(canonicalUserSkillText(raw)).toBe(raw);
    expect(skillMessageBlock(raw)).toEqual({ name: "retired-skill", location: "", body: "", trailing: ` task\n${picks}\n ` });
    expect(readComposerToken(raw, skills)).toBeNull();
    expect(skillMessageBlock("/skill:retired-skill")?.trailing).toBe("");
    expect(skillMessageBlock(expanded("old task", "retired-skill"))?.name).toBe("retired-skill");
  });

  it.each([
    "ordinary uploads/report.pdf", 'look at <skill name="docs" location="/s">\nbody\n</skill>',
    '<skill name="docs" location="/s">\nmissing close', "/compact ",
    '"/skill:docs"', "`/skill:docs`", "/skill: task", "read /skill:docs task",
    '/skill:"docs" task', '<skill name="docs task" location="/s">\nbody\n</skill>',
  ])("does not convert unrecognized text %j", (text) => {
    expect(canonicalUserSkillText(text)).toBe(text);
    expect(skillMessageBlock(text)).toBeNull();
  });
});
