import {describe,it,expect,vi,beforeEach,afterEach} from 'vitest';
import {acceptUIIcons,clearUIIcons,customUIIcons,ensureUIIcon,hiddenUIBuiltins,refreshUIIcons,uiIconLibrary,uiIconPaths} from './uiIcons';
import {starterIconFor} from '@/components/starterIcons';
const id='ui_0123456789abcdef0123456789abcdef';
const view={revision:1,icons:[{id,name:'Private name',path:'M2 3h12v10H2Z',modules:['shortcuts' as const]}],hidden_builtin:{shortcuts:['database'],starters:[],agents:[]},can_manage:true};
beforeEach(clearUIIcons);afterEach(()=>vi.unstubAllGlobals());
describe('shared icon registry and immutable asset lookup',()=>{
 it('scopes selectable icons per module while preserving archived historical glyphs',()=>{
  acceptUIIcons(view);
  expect(customUIIcons('shortcuts').map(i=>i.id)).toEqual([id]);expect(customUIIcons('agents')).toEqual([]);expect(hiddenUIBuiltins('shortcuts')).toEqual(['database']);
  expect(starterIconFor(id)?.body).toContain('M2 3h12v10H2Z');
  acceptUIIcons({...view,icons:[{...view.icons[0]!,archived:true}]});expect(customUIIcons('shortcuts')).toEqual([]);expect(starterIconFor(id)?.body).toContain('M2 3h12v10H2Z');
 });
 it('clears private names immediately and ignores a late authenticated response',async()=>{
  acceptUIIcons(view);let resolve!:(value:Response)=>void;
  vi.stubGlobal('fetch',vi.fn(()=>new Promise<Response>(done=>{resolve=done;})));
  const request=refreshUIIcons();clearUIIcons();resolve(new Response(JSON.stringify(view),{status:200}));await request;
  expect(uiIconLibrary.value).toBeNull();expect(uiIconPaths.value).toEqual({});
 });
 it('public history resolves only an observed exact ID anonymously and rejects unsafe assets',async()=>{
  const fetch=vi.fn().mockResolvedValue(new Response(JSON.stringify({id,path:'M2 3L4 5',width:16,height:16}),{status:200}));vi.stubGlobal('fetch',fetch);
  ensureUIIcon(id);ensureUIIcon(id);ensureUIIcon('../../x');await vi.waitFor(()=>expect(uiIconPaths.value[id]).toBe('M2 3L4 5'));
  expect(fetch).toHaveBeenCalledTimes(1);expect(fetch.mock.calls[0]![0]).toBe(`/icon-assets/${id}.json`);expect(fetch.mock.calls[0]![1]).toMatchObject({credentials:'omit'});expect(uiIconLibrary.value).toBeNull();
  clearUIIcons();fetch.mockResolvedValue(new Response(JSON.stringify({id,path:'<svg onload=alert(1)>',width:16,height:16}),{status:200}));ensureUIIcon(id);await new Promise(resolve=>setTimeout(resolve,0));expect(uiIconPaths.value[id]).toBeUndefined();
 });
 it('a prototype key cannot become an SVG path',()=>{expect(starterIconFor('__proto__')).toBeNull();expect(starterIconFor('toString')).toBeNull();});
});
