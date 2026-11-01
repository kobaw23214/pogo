import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, nextTick, reactive, type Ref } from "vue";
import type { WorkspaceFileTarget } from "./workspaceFileActions";
const host=vi.hoisted(()=>({rename:vi.fn(),remove:vi.fn(),cleanups:[] as (()=>void)[]}));
vi.mock("vue",async original=>({...await original<typeof import("vue")>(),onBeforeUnmount:(fn:()=>void)=>host.cleanups.push(fn),onMounted:()=>{},useSSRContext:()=>({modules:new Set()})}));
vi.mock("vue-i18n",async original=>({...await original<typeof import("vue-i18n")>(),useI18n:()=>({t:(key:string)=>key,d:()=>"date"})}));
vi.mock("@/api/client",()=>({api:{renameFile:host.rename,deleteFile:host.remove},authHeaders:()=>({}),fileDownloadURL:()=>"",fileRawURL:()=>""}));
import FilesPanel from "./FilesPanel.vue";
import { registerWorkspaceMutationGuard } from "./workspaceFileActions";
type Controller={action:Ref<string|null>;actionName:Ref<string>;actionError:Ref<string>;selected:Ref<string>;view:Ref<string>;showAction(kind:"rename"|"delete",target:WorkspaceFileTarget):void;cancelAction():void;submitAction():Promise<void>};
const target={path:"reports/中文.txt",name:"中文.txt",dir:false,size:12,mtime_ms:123456};
const scopes:ReturnType<typeof effectScope>[]=[];
function mount(){const props=reactive({items:[],workspace:"reports",sessionId:"conversation-a"});const scope=effectScope();scopes.push(scope);const c=scope.run(()=>(FilesPanel as unknown as {setup(p:unknown,c:unknown):Controller}).setup(props,{emit:vi.fn(),expose(){}}))!;return {c,props};}
beforeEach(()=>{vi.stubGlobal("window",{addEventListener:vi.fn(),removeEventListener:vi.fn()});host.rename.mockReset().mockResolvedValue({ok:true,kind:"rename",path:target.path,new_path:"reports/改名.txt",is_directory:false});host.remove.mockReset().mockResolvedValue({ok:true,kind:"delete",path:target.path,new_path:"",is_directory:false});});
afterEach(()=>{for(const fn of host.cleanups.splice(0))fn();for(const scope of scopes.splice(0))scope.stop();vi.unstubAllGlobals();});
describe("file context actions",()=>{
  it("opening and cancelling either dialog makes no mutation request",()=>{const {c}=mount();for(const kind of ["rename","delete"] as const){c.showAction(kind,target);c.cancelAction();expect(c.action.value).toBeNull();}expect(host.rename).not.toHaveBeenCalled();expect(host.remove).not.toHaveBeenCalled();});
  it("renames with the exact selected file version and closes its stale preview after success",async()=>{const {c}=mount();c.selected.value=target.path;c.view.value="file";c.showAction("rename",target);c.actionName.value="改名.txt";await c.submitAction();expect(host.rename).toHaveBeenCalledExactlyOnceWith(target.path,"改名.txt",target.mtime_ms,target.size);expect(c.action.value).toBeNull();expect(c.view.value).toBe("tree");expect(c.selected.value).toBe("");});
  it("only sends recursive consent for a directory after explicit confirmation",async()=>{const {c}=mount();const folder={...target,path:"reports",name:"reports",dir:true};c.showAction("delete",folder);expect(host.remove).not.toHaveBeenCalled();await c.submitAction();expect(host.remove).toHaveBeenCalledExactlyOnceWith("reports",true,folder.mtime_ms,folder.size);});
  it("preserves a draft and does not call the backend when an editor blocks the path",async()=>{const remove=registerWorkspaceMutationGuard(()=>false);try{const {c}=mount();c.showAction("delete",target);await c.submitAction();expect(host.remove).not.toHaveBeenCalled();expect(c.action.value).toBe("delete");expect(c.actionError.value).toBe("filesPanel.editingBlocked");}finally{remove();}});
  it("cancels an old-scope dialog instead of mutating after changing sessions",async()=>{const {c,props}=mount();c.showAction("delete",target);props.sessionId="other";await nextTick();await c.submitAction();expect(host.remove).not.toHaveBeenCalled();expect(c.action.value).toBeNull();});
  it("keeps the dialog and error on conflict, releasing the reservation for a retry",async()=>{host.rename.mockRejectedValueOnce(new Error("文件已变化"));const {c}=mount();c.showAction("rename",target);c.actionName.value="改名.txt";await c.submitAction();expect(c.action.value).toBe("rename");expect(c.actionError.value).toBe("文件已变化");await c.submitAction();expect(host.rename).toHaveBeenCalledTimes(2);expect(c.action.value).toBeNull();});
});
