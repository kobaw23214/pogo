import {describe,it,expect,vi,afterEach} from 'vitest';
import {validUIIconPath, validUIIconsView, uiIconsApi, UIIconsAPIError} from './uiIcons';
const id = 'ui_0123456789abcdef0123456789abcdef';
const icon={id,name:'数据库',path:'M2 3h12v10H2Z',modules:['shortcuts']};
const view={revision:1,icons:[icon],hidden_builtin:{shortcuts:[],starters:[],agents:[]},can_manage:true};
afterEach(()=>vi.unstubAllGlobals());
describe('bounded UI icon protocol',()=>{
 it('accepts the narrow complete SVG path grammar and rejects injected or excessive render work',()=>{
  for(const path of ['M2 3h12v10H2Z','m2.5 3.5 11 0M2 3a2 2 0 0 1 3 3Z','M2 3C2 4 3 4 5 6S8 9 9 9','M2 3Q4 5 6 7T8 9']) expect(validUIIconPath(path),path).toBe(true);
  for(const path of ['', ' M2 3','M2 3 ', 'M2', 'M2 3L', 'M2 3L3', 'M2 3,,4 5', 'M2 3,', 'M2 3A2 2 0 2 0 4 5', 'M2 3A-2 2 0 0 1 4 5','M2 3L513 2','M2 3L1e4 2','M2 3"/><script>alert(1)</script>','M2 3url(foo)','M2 3Z'+ 'L2 3'.repeat(256),'M2 3'+' 2 3'.repeat(256)]) expect(validUIIconPath(path),path.slice(0,80)).toBe(false);
 });
 it('validates module scope, identifiers and SVG before displaying a library',()=>{
  expect(validUIIconsView(view)).toBe(true);
  for(const replacement of [{id:'ui_../../secrets'},{path:'M0 0" onload="alert(1)'},{modules:['browser']},{name:'\u0000unsafe'}]) expect(validUIIconsView({...view,icons:[{...icon,...replacement}]})).toBe(false);
 });
 it('saves only the revision and shared draft; conflict carries validated current state',async()=>{
  const fetch=vi.fn().mockResolvedValue(new Response(JSON.stringify(view),{status:200}));vi.stubGlobal('fetch',fetch);
  await uiIconsApi.save(view as never);
  const body=JSON.parse(fetch.mock.calls[0]![1].body);
  expect(body).toEqual({revision:1,icons:[icon],hidden_builtin:view.hidden_builtin});expect(body.can_manage).toBeUndefined();
  fetch.mockResolvedValue(new Response(JSON.stringify({error:'Changed',current:view}),{status:409}));
  await expect(uiIconsApi.save(view as never)).rejects.toMatchObject({status:409,current:view});
 });
 it('never accepts arbitrary generated markup or an empty generation result',async()=>{
  const fetch=vi.fn().mockResolvedValue(new Response(JSON.stringify({icons:[{...icon,path:'<svg onload=alert(1)>'}]}),{status:200}));vi.stubGlobal('fetch',fetch);
  await expect(uiIconsApi.generate('database','model',1)).rejects.toBeInstanceOf(UIIconsAPIError);
 });
});
