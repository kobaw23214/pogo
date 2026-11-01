import { afterEach, describe, expect, it, vi } from 'vitest';
import { effectScope, nextTick, reactive } from 'vue';
import { useAgentWorkflowResources } from './useAgentWorkflowResources';
const host = vi.hoisted(() => ({ call: vi.fn() }));
vi.mock('@/api/agentWorkflows', () => ({ agentWorkflowsApi: { call: host.call } }));
const scopes: ReturnType<typeof effectScope>[] = [];
const flush = async () => { for (let i=0;i<10;i++) await Promise.resolve(); await nextTick(); };
const response = (agent='agent-a', session='session-a') => ({ ok: true, agent_id: agent, resources: { source_session_id: session, skills: [], shortcuts: [], files: [{ path: 'source.md', name:'source.md',size:100 }] } });
function mount() { const props = reactive({ agentId:'agent-a',sessionId:'session-a',visible:true,authEpoch:0 }); const scope=effectScope();scopes.push(scope);return {props,result:scope.run(()=>useAgentWorkflowResources(()=>props))!}; }
afterEach(()=>{for(const scope of scopes.splice(0))scope.stop();host.call.mockReset();vi.useRealTimers();});
describe('workflow resource discovery scope',()=>{
  it('loads only the selected Agent/source-session catalog',async()=>{
    host.call.mockResolvedValue(response());const{result}=mount();await flush();
    expect(host.call.mock.calls[0]!.slice(0,3)).toEqual(['agent-a','read',{action:'resources',source_session_id:'session-a'}]);
    expect(result.catalog.value?.files[0]?.path).toBe('source.md');expect(result.loading.value).toBe(false);
  });
  it('discards a late catalog after source session changes',async()=>{
    let finish!:(value:unknown)=>void;host.call.mockImplementationOnce(()=>new Promise(resolve=>{finish=resolve;})).mockResolvedValue(response('agent-a','session-b'));
    const{props,result}=mount();props.sessionId='session-b';await flush();finish(response());await flush();
    expect((host.call.mock.calls[0]![3] as AbortSignal).aborted).toBe(true);expect(result.catalog.value?.source_session_id).toBe('session-b');
    props.visible=false;await flush();expect(result.catalog.value).toBeNull();
  });
  it('rejects catalogs with another source or unsafe file paths',async()=>{
    host.call.mockResolvedValue(response('agent-a','other-session'));const{result}=mount();await flush();expect(result.catalog.value).toBeNull();expect(result.error.value).toBe('unavailable');
    host.call.mockResolvedValue({...response(),resources:{...response().resources,files:[{path:'../other',name:'other',size:1}]}});await result.refresh();expect(result.catalog.value).toBeNull();
  });
  it('ends a stalled request visibly instead of staying in loading state',async()=>{
    vi.useFakeTimers();host.call.mockImplementation((_aid:string,_group:string,_body:unknown,signal:AbortSignal)=>new Promise((_resolve,reject)=>signal.addEventListener('abort',()=>reject(new DOMException('Aborted','AbortError')))));
    const{result}=mount();await vi.advanceTimersByTimeAsync(10_000);await flush();expect(result.loading.value).toBe(false);expect(result.error.value).toBe('unavailable');
  });
});
