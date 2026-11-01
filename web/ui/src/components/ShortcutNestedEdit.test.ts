import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive, type Ref } from "vue";
import type { ShortcutSnapshot } from "@/api/shortcuts";
import type { ComposerPart, ComposerShortcutPart } from "@/agent/composerParts";
import { shortcutComposerPart } from "@/agent/composerParts";
const host = vi.hoisted(() => ({ cleanups: [] as (() => void)[], register: vi.fn(() => () => {}) }));
vi.mock("vue", async original => ({ ...await original<typeof import("vue")>(), inject: () => null, useId: () => "nested-test", useSSRContext: () => ({ modules: new Set() }), onBeforeUnmount: (fn: () => void) => host.cleanups.push(fn) }));
vi.mock("vue-i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/agent/studioNavigation", () => ({ registerStudioNavigationGuard: host.register }));
vi.mock("./InlineComposer.vue", () => ({ default: {} }));
vi.mock("./ShortcutContent.vue", () => ({ default: {} }));
vi.mock("./ShortcutLanguageSelect.vue", () => ({ default: {} }));
vi.mock("./studio/StarterIconPicker.vue", () => ({ default: {} }));
vi.mock("./OfficeReferenceDialog.vue", () => ({ default: {} }));
vi.mock("./ShortcutReferenceDialog.vue", () => ({ default: {} }));
vi.mock("./ConfirmDialog.vue", () => ({ default: {} }));
import ShortcutEditorDialog from "./ShortcutEditorDialog.vue";
interface Controller {
 draft: Ref<ShortcutSnapshot>; draftParts: Ref<ComposerPart[]>; nestedDraft: Ref<ShortcutSnapshot|null>; nestedPreview: Ref<ShortcutSnapshot|null>;
 nestedEditor: Ref<{confirmLeave():Promise<boolean>;busy:boolean}|null>; canAddNested: Ref<boolean>; structureError: Ref<string>; saveError: Ref<string>;
 draftEditor: Ref<{getSelection():{from:number;to:number};setSelection(value:unknown):void;focus():void}|null>;
 leaveConfirm: Ref<{ask(value:unknown):Promise<boolean>}|null>;
 activateReference(part:ComposerShortcutPart):void;editNestedHere():void;newNested():void;
 applyNestedSnapshot(item:ShortcutSnapshot):Promise<void>;nestedVisibility(open:boolean):Promise<void>;validateNestedSnapshot(item:ShortcutSnapshot):string;
 saveDraft():Promise<void>;confirmLeave():Promise<boolean>;chooseNested(id:string):Promise<void>;
}
const scopes:ReturnType<typeof effectScope>[]=[];
const source:ShortcutSnapshot={id:"shared",title:"来源",content:"原始正文",language:"text",icon:"book"};
function mount(extra:Record<string,unknown>={}) {
 const props=reactive({modelValue:true,item:{id:"root",title:"父卡",content:"父卡正文"} as ShortcutSnapshot,existing:true,library:[] as ShortcutSnapshot[],skills:[],sessionId:"session-a",scopeKey:"agent-a",disabled:false,snapshotOnly:false,ancestorIds:[] as string[],save:vi.fn().mockResolvedValue(undefined),...extra});
 const scope=effectScope();scopes.push(scope);const emit=vi.fn();
 const c=scope.run(()=>(ShortcutEditorDialog as unknown as {setup(props:unknown,context:unknown):Controller}).setup(props,{emit,expose(){}}))!;
 return {c,props,emit};
}
function open(c:Controller,part:ComposerShortcutPart) { c.activateReference(part);c.editNestedHere();expect(c.nestedDraft.value).toBeTruthy(); }
beforeEach(()=>{host.register.mockClear();vi.stubGlobal("window",{addEventListener:vi.fn(),removeEventListener:vi.fn()});});
afterEach(()=>{for(const fn of host.cleanups.splice(0))fn();for(const scope of scopes.splice(0))scope.stop();vi.unstubAllGlobals();});

describe("edit this embedded occurrence controller",()=>{
 it("applies only the selected repeated card, keeps sibling IDs and saves the root once",async()=>{
  const {c,props}=mount({library:[source]});const first=shortcutComposerPart(source),second=shortcutComposerPart(source);
  c.draftParts.value=[first,{type:"text",text:"父稿尚未保存"},second];const beforeSource=JSON.stringify(props.library),ids=c.draftParts.value.filter(p=>p.type!=="text").map(p=>p.id);
  open(c,c.draftParts.value[2] as ComposerShortcutPart);
  await c.applyNestedSnapshot({...c.nestedDraft.value!,title:"本处新标题",content:"新正文",icon:"",language:"sql"});
  expect(c.draftParts.value[0]).toMatchObject(first);expect(c.draftParts.value[1]).toEqual({type:"text",text:"父稿尚未保存"});
  expect(c.draftParts.value[2]).toMatchObject({id:second.id,title:"本处新标题",text:"新正文",language:"sql"});
  expect(c.draftParts.value.filter(p=>p.type!=="text").map(p=>p.id)).toEqual(ids);expect(JSON.stringify(props.library)).toBe(beforeSource);expect(props.save).not.toHaveBeenCalled();
  await c.nestedVisibility(false);await c.saveDraft();expect(props.save).toHaveBeenCalledTimes(1);
  expect(props.save.mock.calls[0][0].parts).toMatchObject([{title:"来源"},{text:"父稿尚未保存"},{title:"本处新标题"}]);
 });
 it("cancel drops only the detached child draft and restores the original cursor",async()=>{
  const {c,props}=mount();const part=shortcutComposerPart(source);c.draftParts.value=[part];
  const cursor={getSelection:()=>({from:2,to:2}),setSelection:vi.fn(),focus:vi.fn()};c.draftEditor.value=cursor;
  const before=JSON.stringify(c.draftParts.value);open(c,part);c.nestedDraft.value!.title="未应用";
  await c.nestedVisibility(false);expect(JSON.stringify(c.draftParts.value)).toBe(before);expect(props.save).not.toHaveBeenCalled();expect(cursor.setSelection).toHaveBeenCalledWith({from:2,to:2});expect(cursor.focus).toHaveBeenCalledOnce();
 });
 it.each(["scope","session","reopen","target","disabled"])("rejects a late apply after %s changes",async kind=>{
  const {c,props}=mount();const part=shortcutComposerPart(source);c.draftParts.value=[part];open(c,part);const replacement={...c.nestedDraft.value!,content:"迟到修改"};
  if(kind==="scope")props.scopeKey="agent-b";if(kind==="session")props.sessionId="session-b";
  if(kind==="reopen"){props.modelValue=false;await nextTick();props.modelValue=true;await nextTick();}
  if(kind==="target")c.draftParts.value=[{...part,text:"另一次编辑"}];if(kind==="disabled")props.disabled=true;
  const before=JSON.stringify(c.draftParts.value);await expect(c.applyNestedSnapshot(replacement)).rejects.toThrow();expect(JSON.stringify(c.draftParts.value)).toBe(before);expect(props.save).not.toHaveBeenCalled();
 });
 it("allows unrelated parent prose edits while fencing conflicting changes to the target",async()=>{
  const {c}=mount();const part=shortcutComposerPart(source);c.draftParts.value=[part];open(c,part);c.draftParts.value.push({type:"text",text:"保留父卡新输入"});
  await c.applyNestedSnapshot({...c.nestedDraft.value!,content:"本处更改"});expect(c.draftParts.value[1]).toEqual({type:"text",text:"保留父卡新输入"});
 });
 it("checks the whole root node and byte budgets, not only a locally valid child",()=>{
  const {c}=mount();const part=shortcutComposerPart(source);c.draftParts.value=[...Array.from({length:500},()=>({type:"skill",id:crypto.randomUUID(),name:"office",label:"office"} as ComposerPart)),part];open(c,part);
  expect(c.validateNestedSnapshot({...source,parts:Array.from({length:12},()=>({type:"skill",name:"office"}))})).toBe("nodes");
  c.draftParts.value=[{type:"text",text:"a".repeat(100000)},part];open(c,part);
  expect(c.validateNestedSnapshot({...source,content:"b".repeat(35000),parts:[{type:"text",text:"b".repeat(35000)},{type:"skill",name:"office"}]})).toBe("bytes");
 });
 it("includes root in the five-level limit and refuses an ancestor reference",async()=>{
  const {c}=mount({snapshotOnly:true,ancestorIds:["root","two","three","four"],item:{id:"five",title:"第五层",content:"内容"},library:[{id:"root",title:"父卡",content:"不可循环"}]});
  expect(c.canAddNested.value).toBe(false);c.newNested();expect(c.nestedDraft.value).toBeNull();
  await c.chooseNested("root");expect(c.saveError.value).toBe("nestedShortcuts.cycle");expect(c.draftParts.value).toEqual([{type:"text",text:"内容"}]);expect(host.register).not.toHaveBeenCalled();
 });
 it("creates a child only on apply and rejects a changed insertion draft",async()=>{
  const {c,props}=mount();const before=JSON.stringify(c.draftParts.value);c.newNested();expect(JSON.stringify(c.draftParts.value)).toBe(before);const child={...c.nestedDraft.value!,title:"新子卡",content:"正文"};
  c.draftParts.value=[{type:"text",text:"新输入"}];await expect(c.applyNestedSnapshot(child)).rejects.toThrow("nestedShortcuts.changedDraft");expect(props.save).not.toHaveBeenCalled();
  await c.nestedVisibility(false);c.newNested();await c.applyNestedSnapshot({...c.nestedDraft.value!,content:"正文"});expect(c.draftParts.value).toMatchObject([{type:"text",text:"新输入"},{type:"shortcut",title:"nestedShortcuts.newTitle",text:"正文"}]);expect(props.save).not.toHaveBeenCalled();
 });
 it("a dirty descendant can veto root navigation and root save before any root callback",async()=>{
  const {c,props}=mount();c.newNested();c.nestedEditor.value={confirmLeave:vi.fn().mockResolvedValue(false),busy:false};c.leaveConfirm.value={ask:vi.fn().mockResolvedValue(true)};
  expect(await c.confirmLeave()).toBe(false);expect(c.leaveConfirm.value.ask).not.toHaveBeenCalled();await c.saveDraft();expect(props.save).not.toHaveBeenCalled();
 });
});
