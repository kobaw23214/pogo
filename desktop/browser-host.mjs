import { randomUUID, createHash } from 'node:crypto';
import { ScopedContext } from './scoped-context.mjs';
import { inspectNativeNode } from './native-inspector.mjs';

const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
const validURL = value => { try { const u=new URL(value);return ['http:','https:'].includes(u.protocol)&&!u.username&&!u.password; } catch{return false;} };
const popupURL = value => value === 'about:blank' || validURL(value);
const highlightConfig={showInfo:true,contentColor:{r:69,g:140,b:235,a:.2},borderColor:{r:69,g:140,b:235,a:.8}};

// The one owner of website WebContentsViews. Neither Studio nor websites receive
// CDP endpoints, Electron objects, worker credentials or arbitrary IPC methods.
export class DesktopBrowserHost {
  diagnose(event,record,details={}) {
    if(process.env.POGO_DESKTOP_DIAGNOSTICS!=='1')return;
    const wc=record.view.webContents;
    console.error(JSON.stringify({desktopDiagnostic:event,webContents:wc.id,epoch:record.pickerEpoch,picking:record.picking,
      interactive:this.interactive(record),focused:!wc.isDestroyed()&&wc.isFocused(),...details}));
  }
  constructor({electron,browser,window,studioOrigin,namespace}) {
    Object.assign(this,{electron,browser,window,studioOrigin,namespace});
    this.owners=new Map();this.surfaces=new Map();this.closed=false;this.targetIDs=new WeakMap();
    this.shield=new electron.WebContentsView({webPreferences:{sandbox:true,contextIsolation:true,nodeIntegration:false}});
    this.shield.setBackgroundColor('#00000000');
    window.contentView.addChildView(this.shield);this.shield.setVisible(false);
    this.shield.webContents.loadURL('data:text/html;charset=utf-8,'+encodeURIComponent('<!doctype html><html><body tabindex="0" style="margin:0;background:transparent;height:100vh;outline:none"><div style="position:absolute;bottom:8px;left:8px;padding:5px 9px;border-radius:5px;background:#202124c9;color:white;font:12px system-ui;pointer-events:none">接管后可直接操作网页</div></body></html>'));
    this.timer=setInterval(()=>this.publish(),250);this.timer.unref();
  }
  factory=async({id,clientSessionId,profileKey})=>{
    if(this.closed)throw new Error('Desktop browser is offline');
    const key=profileKey||createHash('sha256').update(this.namespace+':'+id).digest('hex');
    for(const owner of this.owners.values())if(owner.profileKey===key&&!owner.closed)throw new Error('Desktop profile is already bound to another live browser session');
    const partition='persist:pogo-'+createHash('sha256').update(this.namespace+':'+key).digest('hex');
    const es=this.electron.session.fromPartition(partition);
    es.enableNetworkEmulation({offline:false});
    es.setPermissionCheckHandler(()=>false);es.setPermissionRequestHandler((_w,_p,callback)=>callback(false));
    const context=new ScopedContext({browserContext:this.browser.contexts()[0],electronSession:es});
    const owner={id,clientSessionId:clientSessionId||id,profileKey:key,electronSession:es,context,records:new Map(),pendingRecords:new Set(),closed:false,closing:false,inputSuspended:0,runtime:null};
    this.owners.set(id,owner);
    const host={
      displayMode:'desktop',
      bindSession:runtime=>{
        owner.runtime=runtime;
        for(const name of ['setControl','cancel','close']){
          const original=runtime[name].bind(runtime);
          runtime[name]=async(...args)=>{
            owner.inputSuspended++;this.layout();
            try {
              for(const record of owner.records.values())if(record.view.webContents.isDevToolsOpened())await this.closeDevTools(record);
              return await original(...args);
            }finally{owner.inputSuspended--;this.layout();}
          };
        }
      },
      ownsPage:page=>owner.records.has(page)&&!owner.closed,
      newPage:()=>this.newPage(owner),
      closePage:page=>this.closePage(owner,page),
      setOffline:async offline=>{if(owner.closed)throw new Error('Desktop profile closed');es.enableNetworkEmulation({offline});},
      getViewport:page=>{const record=owner.records.get(page);if(!record)throw new Error('Foreign desktop page');const b=record.view.getBounds(),z=record.view.webContents.getZoomFactor();return {width:Math.max(1,Math.round(b.width/z)),height:Math.max(1,Math.round(b.height/z))};},
      close:()=>this.closeOwner(owner),
    };
    return {context,host};
  };
  ownedRecords(owner) {
    return [...new Set([...owner.records.values(), ...owner.pendingRecords])];
  }
  forgetRecord(record) {
    const {owner,view}=record;
    owner.pendingRecords.delete(record);
    if(record.page){owner.context.unregister(record.page);owner.records.delete(record.page);}
    if(!record.viewRemoved&&!this.window.isDestroyed()){
      this.window.contentView.removeChildView(view);record.viewRemoved=true;
    }
  }
  securePreferences(owner) {
    return {session:owner.electronSession,nodeIntegration:false,nodeIntegrationInWorker:false,
      nodeIntegrationInSubFrames:false,contextIsolation:true,sandbox:true,webSecurity:true,
      allowRunningInsecureContent:false};
  }
  canCreate(owner, popup=false) {
    // No tab-count cap: the host's own memory is the practical ceiling, and a
    // hard number only ever fired on the user's explicit "+".
    return !owner.closed&&!owner.closing&&!this.closed&&
      (!popup||owner.runtime?.phase==='ready'&&!owner.runtime.closed);
  }
  createRecord(owner, options={}, opener=null) {
    if(!this.canCreate(owner,Boolean(opener)))throw new Error('Desktop browser scope is closed');
    // Adopt Electron's actual popup WebContents. Recreating it and navigating
    // to the URL would discard POST data, window.opener and synchronous writes.
    const view=new this.electron.WebContentsView({...(options.webContents?{webContents:options.webContents}:{}),webPreferences:this.securePreferences(owner)});
    const wc=view.webContents,record={owner,view,page:null,surface:null,picking:false,pickerEpoch:0,lastMouse:null,closed:false,
      opener,generation:owner.runtime?.generation,bindFlight:null,closeFlight:null};
    owner.pendingRecords.add(record);
    view.setBounds({x:0,y:0,width:1280,height:800});view.setVisible(false);this.window.contentView.addChildView(view);
    this.window.contentView.removeChildView(this.shield);this.window.contentView.addChildView(this.shield);
    wc.setWindowOpenHandler(details=>{
      if(!popupURL(details.url)||!this.canCreate(owner,true))return {action:'deny'};
      return {action:'allow',overrideBrowserWindowOptions:{webPreferences:this.securePreferences(owner)},createWindow:options=>{
        const child=this.createRecord(owner,options,record);
        this.bindRecord(child).then(async()=>{
          // Electron defers background-tab creation when no WebContents was
          // supplied. This is its first load, never a replay of a native load.
          if(!options.webContents&&details.disposition==='background-tab'){
            const post=details.postBody;
            await child.view.webContents.loadURL(details.url,{httpReferrer:details.referrer,
              ...(post?{postData:post.data,extraHeaders:`Content-Type: ${post.contentType}${post.boundary?'; boundary='+post.boundary:''}\r\n`}:{})});
          }
        }).catch(error=>this.ownerError(owner,error));
        return child.view.webContents;
      }};
    });
    const navigation=(event,url)=>{if(!popupURL(url))event.preventDefault();};
    wc.on('will-navigate',navigation);wc.on('will-redirect',navigation);
    wc.on('before-input-event',(event,input)=>{
      if(!this.interactive(record)||record.picking)event.preventDefault();
      // The website owns keyboard focus, so Escape never reaches Studio's
      // Vue listener. End native inspect mode in the main-process owner.
      if(record.picking&&input?.key==='Escape'&&input.type==='keyDown')this.stopPicker(record,'escape').catch(error=>this.ownerError(owner,error));
    });
    wc.on('before-mouse-event',(event,mouse)=>{
      if(record.picking||mouse.type==='mouseDown'||mouse.type==='mouseUp')this.diagnose('mouse',record,{type:mouse.type,x:mouse.x,y:mouse.y});
      if(record.picking&&Number.isFinite(mouse.x)&&Number.isFinite(mouse.y))record.lastMouse={x:mouse.x/wc.getZoomFactor(),y:mouse.y/wc.getZoomFactor()};
      // Overlay only consumes a click after its first pointer movement. A
      // first click without hover must never submit the underlying webpage.
      // Keep the mouse-up fenced even if the read finishes before it arrives.
      if(mouse.type==='mouseUp'&&(record.pickerPointerDown||record.picking)){
        event.preventDefault();record.pickerPointerDown=false;return;
      }
      if(mouse.type==='mouseDown'&&(record.pickerPointerDown||record.picking)){
        event.preventDefault();record.pickerPointerDown=true;
        if(record.picking&&this.interactive(record)&&mouse.button==='left'&&record.lastMouse)this.selectAtPoint(record,record.lastMouse).catch(()=>{});
        return;
      }
      const runtime=owner.runtime,meta=[...(runtime?.pages.values()??[])].find(page=>page.page===record.page);
      const agentCommand=!!(meta&&runtime.controlFor(meta).mode==='agent'&&runtime.operationTouches(meta)&&!runtime.contextBlocked());
      if(!this.interactive(record)&&!agentCommand)event.preventDefault();
    });
    wc.on('focus',()=>{if(!this.interactive(record)&&!this.shield.webContents.isDestroyed())this.shield.webContents.focus();});
    wc.on('did-navigate',()=>{if(record.picking)this.stopPicker(record,'navigation').catch(()=>{});});
    wc.on('destroyed',()=>{
      record.closed=true;this.forgetRecord(record);
      if(record.surface)this.emit(record.surface,{type:'error',message:'浏览器标签页已关闭'});
    });
    wc.debugger.on('message',(_event,method,params,sessionId)=>{
      if(method.startsWith('Overlay.'))this.diagnose('overlay-event',record,{method,childTarget:Boolean(sessionId)});
      if(method==='Overlay.inspectNodeRequested'&&record.picking){
        this.selected(record,params.backendNodeId,sessionId).catch(()=>{});
      }
    });
    wc.debugger.on('detach',(_event,reason)=>this.diagnose('debugger-detached',record,{reason:String(reason).slice(0,80)}));
    return record;
  }
  async newPage(owner) {
    const record=this.createRecord(owner);
    try {
      console.error('desktop lifecycle: create '+record.view.webContents.id);
      await record.view.webContents.loadURL('about:blank');
      console.error('desktop lifecycle: blank-ready '+record.view.webContents.id);
      return await this.bindRecord(record);
    }catch(error){await this.closeRecord(record);throw error;}
  }
  bindRecord(record) {
    if(record.bindFlight)return record.bindFlight;
    const {owner,view}=record,wc=view.webContents;
    record.bindFlight=(async()=>{
      try {
        if(!wc.debugger.isAttached())wc.debugger.attach('1.3');
        const {targetInfo}=await wc.debugger.sendCommand('Target.getTargetInfo');
        const deadline=Date.now()+10000;
        while(!record.page&&Date.now()<deadline){
          if(record.closed||owner.closed||owner.closing||this.closed)throw new Error('Desktop scope closed during page binding');
          search:for(const ctx of this.browser.contexts())for(const page of ctx.pages()){
            if(page.isClosed())continue;
            const known=this.targetIDs.get(page);
            if(known){if(known===targetInfo.targetId){record.page=page;break search;}continue;}
            let client;
            try {client=await ctx.newCDPSession(page);const info=await client.send('Target.getTargetInfo');this.targetIDs.set(page,info.targetInfo.targetId);if(info.targetInfo.targetId===targetInfo.targetId){record.page=page;break search;}}
            catch{}finally{await client?.detach().catch(()=>{});}
          }
          if(!record.page)await wait(20);
        }
        if(!record.page)throw new Error('Visible desktop page could not be bound to Playwright');
        if(record.closed||owner.closed||owner.closing||this.closed||owner.runtime?.generation!==record.generation)throw new Error('Desktop scope changed during page binding');
        owner.records.set(record.page,record);
        await owner.context.register(record.page);
        if(record.closed||owner.closed||owner.closing||owner.runtime?.generation!==record.generation)throw new Error('Desktop scope changed during page registration');
        owner.pendingRecords.delete(record);
        // The transport popup event can precede scoped registration. Preserve
        // the native opener relationship after both owned Pages are attached.
        if(record.opener?.page)owner.runtime?.recordPopup(record.opener.page,record.page);
        console.error('desktop lifecycle: registered '+wc.id);
        return record.page;
      }catch(error){await this.closeRecord(record);throw error;}
    })();
    return record.bindFlight;
  }
  async closePage(owner,page) {
    const record=owner.records.get(page);if(!record)throw new Error('Page is outside this desktop browser scope');
    return this.closeRecord(record);
  }
  async closeRecord(record) {
    if(record.closeFlight)return record.closeFlight;
    const {owner,view}=record;
    record.closeFlight=(async()=>{
      // Destruction must not wait for an Overlay command on a paused renderer.
      if(record.picking&&record.surface)this.emit(record.surface,{type:'picker-ended'});
      record.pickerEpoch++;record.picking=false;record.lastMouse=null;
      if(!view.webContents.isDestroyed()){
        const wc=view.webContents;
        console.error('desktop lifecycle: close '+wc.id);
        await new Promise((resolve,reject)=>{
          const done=()=>{clearTimeout(timer);resolve();};
          const timer=setTimeout(()=>{wc.removeListener('destroyed',done);reject(new Error('Desktop page destruction was not confirmed'));},3000);
          wc.once('destroyed',done);
          try{wc.close({waitForBeforeUnload:false});}catch(error){clearTimeout(timer);wc.removeListener('destroyed',done);reject(error);}
        });
      }
      this.forgetRecord(record);record.closed=true;
      console.error('desktop lifecycle: destroyed');this.layout();
    })();
    try{return await record.closeFlight;}finally{record.closeFlight=null;}
  }
  async closeOwner(owner) {
    if(owner.closed)return;if(owner.closingFlight)return owner.closingFlight;
    owner.closing=true;this.layout();
    owner.closingFlight=(async()=>{
      const results=await Promise.allSettled(this.ownedRecords(owner).map(record=>this.closeRecord(record)));
      const failure=results.find(r=>r.status==='rejected');if(failure)throw failure.reason;
      await owner.context.close();await owner.electronSession.cookies.flushStore();
      owner.closed=true;this.owners.delete(owner.id);
    })();
    try{await owner.closingFlight;}finally{owner.closingFlight=null;}
  }
  resolve(binding) {
    if(!binding||typeof binding.session_id!=='string'||typeof binding.context_ref!=='string'||typeof binding.tab_id!=='string')throw new Error('Invalid desktop tab binding');
    const owner=[...this.owners.values()].find(o=>!o.closed&&o.clientSessionId===binding.session_id&&o.runtime?.contextRef===binding.context_ref);
    if(!owner)throw new Error('Desktop browser session is unavailable');
    owner.runtime.assertLive();const meta=owner.runtime.selectedPage(binding),record=owner.records.get(meta.page);
    if(!record||record.closed)throw new Error('Desktop page binding is unavailable');
    return {owner,record,meta};
  }
  interactive(record) {
    const surface=record.surface,r=record.owner.runtime,c=surface?.claim;
    const meta=[...(r?.pages.values()??[])].find(page=>page.page===record.page),owner=meta&&r.controlFor(meta);
    return !!(surface&&c&&c.visible&&!c.blocked&&!record.closed&&!record.owner.closing&&!record.owner.inputSuspended&&!r?.closed&&r?.phase==='ready'&&owner?.phase==='ready'&&owner.mode==='human'&&owner.lease&&c.lease_token===owner.lease&&c.generation===r.generation&&c.control_epoch===owner.epoch&&!r.operationTouches(meta)&&!r.contextBlocked());
  }
  emit(surface,event) {
    if(!surface||!this.surfaces.has(surface.id)||this.window.isDestroyed())return;
    this.window.webContents.send('pogo:desktop-browser:event',{surface_id:surface.id,...surface.binding,...event});
  }
  ownerError(owner,error){for(const surface of this.surfaces.values())if(surface.record.owner===owner)this.emit(surface,{type:'error',message:String(error.message).slice(0,300)});}
  layout() {
    if(this.window.isDestroyed())return;
    let chosen=null;
    for(const surface of this.surfaces.values())if(surface.claim?.visible&&!surface.record.closed)chosen=surface;
    for(const owner of this.owners.values())for(const record of owner.records.values()){
      const visible=Boolean(chosen&&record.surface===chosen);
      if(record.view.getVisible()!==visible)record.view.setVisible(visible);
    }
    const shield=chosen&&!this.interactive(chosen.record);
    if(chosen){this.shield.setBounds(chosen.record.view.getBounds());if(!shield&&this.shield.webContents.isFocused())chosen.record.view.webContents.focus();}
    if(this.shield.getVisible()!==Boolean(shield))this.shield.setVisible(Boolean(shield));
  }
  publish() {
    if(this.closed)return;
    for(const surface of this.surfaces.values()){
      try {
        const {owner,record}=this.resolve(surface.binding);
        if(!this.interactive(record)&&record.view.webContents.isDevToolsOpened())record.view.webContents.closeDevTools();
        if(record.picking&&!this.interactive(record))this.stopPicker(record,'control-unavailable').catch(()=>{});
        const state=owner.runtime.state(surface.binding),serialized=JSON.stringify(state);
        if(serialized!==surface.lastState){surface.lastState=serialized;this.emit(surface,{type:'state',state});}
      }catch(error){if(!surface.unavailable){surface.unavailable=true;this.emit(surface,{type:'error',message:String(error.message)});}}
    }
    this.layout();
  }
  async stopPicker(record,reason='requested') {
    const was=record.picking;record.pickerEpoch=(record.pickerEpoch||0)+1;record.picking=false;record.lastMouse=null;
    if(!was)return;
    this.diagnose('picker-stop',record,{reason});
    if(record.surface)this.emit(record.surface,{type:'picker-ended'});
    const wc=record.view.webContents;
    if(!wc.isDestroyed()&&wc.debugger.isAttached())await wc.debugger.sendCommand('Overlay.setInspectMode',{mode:'none',highlightConfig});
  }
  async startPicker(record) {
    if(!this.interactive(record))throw new Error('请先接管浏览器');
    const epoch=(record.pickerEpoch||0)+1;
    record.pickerEpoch=epoch;record.picking=true;record.lastMouse=null;
    this.diagnose('picker-start',record);
    // Track installation from its first command. Cancellation can happen at
    // every await, including before Overlay has acknowledged inspect mode.
    const current=()=>{if(record.pickerEpoch!==epoch||!record.picking||!this.interactive(record))throw new Error('页面控制权或元素引用模式已变化');};
    const wc=record.view.webContents;
    try {
      if(!wc.debugger.isAttached())wc.debugger.attach('1.3');
      await wc.debugger.sendCommand('DOM.enable');current();
      await wc.debugger.sendCommand('Overlay.enable');current();
      await wc.debugger.sendCommand('Overlay.setInspectMode',{mode:'searchForNode',highlightConfig});current();
      this.diagnose('picker-installed',record);
    }catch(error){
      // A stale installation must not stop a newer selection request.
      if(record.pickerEpoch===epoch)await this.stopPicker(record,'installation-failed');
      throw error;
    }
  }
  async closeDevTools(record) {
    const wc=record.view.webContents;if(wc.isDestroyed()||!wc.isDevToolsOpened())return;
    await new Promise((resolve,reject)=>{const done=()=>{clearTimeout(timer);resolve();};const timer=setTimeout(()=>{wc.removeListener('devtools-closed',done);reject(new Error('DevTools close was not confirmed'));},3000);wc.once('devtools-closed',done);wc.closeDevTools();});
  }
  async selectAtPoint(record,point) {
    const epoch=record.pickerEpoch,surface=record.surface;
    let live=true,timer;
    try {
      if(record.hitTestFlight)throw new Error('前一次元素定位尚未结束，请稍后重新引用');
      const {owner,meta}=this.resolve(surface.binding),generation=owner.runtime.generation,documentVersion=meta.version;
      const check=()=>{if(!live||record.pickerEpoch!==epoch||!record.picking||record.surface!==surface||!this.interactive(record)||owner.runtime.generation!==generation||meta.version!==documentVersion)throw new Error('页面或元素引用模式已变化');};
      const raw=(async()=>{
        check();
        const cdp=record.view.webContents.debugger;
        const node=await cdp.sendCommand('DOM.getNodeForLocation',{x:Math.round(point.x),y:Math.round(point.y),includeUserAgentShadowDOM:true,ignorePointerEventsNone:false});check();
        const {frameTree}=await cdp.sendCommand('Page.getFrameTree');check();
        // Root-target coordinates cannot establish an out-of-process child
        // target binding. Refuse child frames instead of quoting their owner.
        if(!node.frameId||node.frameId!==frameTree?.frame?.id)throw new Error('暂无法确认跨框架元素，请选择主页面元素');
        if(!Number.isSafeInteger(node.backendNodeId)||node.backendNodeId<1)throw new Error('当前位置没有可引用的元素');
        await this.selected(record,node.backendNodeId,undefined,point);
      })();
      record.hitTestFlight=raw;
      raw.finally(()=>{if(record.hitTestFlight===raw)record.hitTestFlight=null;}).catch(()=>{});
      await Promise.race([raw,new Promise((_,reject)=>{timer=setTimeout(()=>reject(new Error('元素定位超时，请重新引用')),1500);})]);
    }catch(error){
      if(record.pickerEpoch===epoch){if(surface)this.emit(surface,{type:'error',message:String(error.message)});await this.stopPicker(record,'hit-test-failed');}
      throw error;
    }finally{live=false;clearTimeout(timer);}
  }
  async selected(record,backendNodeId,sessionId,selectedPoint) {
    if(record.selecting)return;record.selecting=true;
    const selectionEpoch=record.pickerEpoch;
    try {
      if(!this.interactive(record))throw new Error('请先接管浏览器');
      const surface=record.surface,{owner,meta}=this.resolve(surface.binding),generation=owner.runtime.generation,documentVersion=meta.version,pickerEpoch=record.pickerEpoch;
      const current=()=>record.surface===surface&&record.picking&&record.pickerEpoch===pickerEpoch&&this.interactive(record);
      await owner.runtime.refreshRevision(owner.runtime.observation(),meta);
      const version=owner.runtime.version(meta);
      const debuggerAPI=record.view.webContents.debugger;
      const resolved=await debuggerAPI.sendCommand('DOM.resolveNode',{backendNodeId,objectGroup:'pogo-selection'},sessionId||undefined);
      let expectedIdentity;
      try {
        const result=await debuggerAPI.sendCommand('Runtime.callFunctionOn',{objectId:resolved.object.objectId,functionDeclaration:'function(key) { return globalThis[key]?.summarize(this)?.identity; }',arguments:[{value:owner.runtime.picker.key}],returnByValue:true},sessionId||undefined);
        expectedIdentity=result.result?.value;
      }finally{if(resolved.object.objectId)await debuggerAPI.sendCommand('Runtime.releaseObject',{objectId:resolved.object.objectId},sessionId||undefined).catch(()=>{});}
      if(typeof expectedIdentity!=='string'||!expectedIdentity){
        if(sessionId)throw new Error('暂无法确认跨框架元素的原生坐标，请选择主页面元素');
        const pick=await inspectNativeNode({send:(method,params)=>debuggerAPI.sendCommand(method,params),backendNodeId,runtime:owner.runtime,meta,snapshotStamp:{surface,generation,documentVersion},
          checkCurrent:stamp=>current()&&record.surface===stamp.surface&&owner.runtime.generation===stamp.generation&&meta.version===stamp.documentVersion});
        if(!current())throw new Error('页面控制权已变化');
        this.emit(surface,{type:'pick',pick});return;
      }
      let point=selectedPoint||record.lastMouse;
      if(!point){const {model}=await record.view.webContents.debugger.sendCommand('DOM.getBoxModel',{backendNodeId},sessionId||undefined);const q=model.content;point={x:(q[0]+q[2]+q[4]+q[6])/4,y:(q[1]+q[3]+q[5]+q[7])/4};}
      if(meta.version!==documentVersion||owner.runtime.generation!==generation||!current())throw new Error('页面已变化，请重新引用');
      const pick=await owner.runtime.picker.pick({...point,tab_id:meta.id,context_ref:owner.runtime.contextRef,version},{expectedIdentity});
      if(!current())throw new Error('页面控制权已变化');
      this.emit(surface,{type:'pick',pick});
    }catch(error){
      if(record.pickerEpoch===selectionEpoch&&record.surface)this.emit(record.surface,{type:'error',message:String(error.message)});
      throw error;
    }finally{try{if(record.pickerEpoch===selectionEpoch)await this.stopPicker(record,'selection-finished');}finally{record.selecting=false;}}
  }
  installIPC(ipcMain) {
    const invalidate=()=>{
      for(const surface of this.surfaces.values()){surface.record.surface=null;surface.record.view.setVisible(false);this.stopPicker(surface.record).catch(()=>{});this.closeDevTools(surface.record).catch(()=>{});}
      this.surfaces.clear();this.shield.setVisible(false);
    };
    this.window.webContents.on('did-start-navigation',(_event,_url,inPlace,isMainFrame)=>{if(isMainFrame&&!inPlace)invalidate();});
    this.window.webContents.on('render-process-gone',invalidate);
    const authorize=event=>{if(this.window.isDestroyed()||event.sender!==this.window.webContents||event.senderFrame!==this.window.webContents.mainFrame||new URL(event.senderFrame.url).origin!==this.studioOrigin)throw new Error('Trusted Studio main frame required');};
    const handle=(name,fn)=>ipcMain.handle('pogo:desktop-browser:'+name,(event,body)=>{authorize(event);return fn(body);});
    handle('attach',body=>{
      if(typeof body?.view_id!=='string'||body.view_id.length>160)throw new Error('Invalid desktop surface ID');
      const {record}=this.resolve(body);
      if(record.surface)this.surfaces.delete(record.surface.id);
      const surface={id:randomUUID(),binding:{session_id:body.session_id,context_ref:body.context_ref,tab_id:body.tab_id},record,revision:-1,claim:null};
      this.surfaces.set(surface.id,surface);record.surface=surface;return {surface_id:surface.id};
    });
    handle('update',async body=>{
      const surface=this.surfaces.get(body?.surface_id);if(!surface)throw new Error('Unknown desktop surface');
      if(!Number.isSafeInteger(body.revision)||body.revision<0)throw new Error('Invalid surface revision');
      if(body.revision<=surface.revision)return {interactive:this.interactive(surface.record)};
      surface.revision=body.revision;
      const b=body.bounds,z=this.window.webContents.getZoomFactor(),size=this.window.getContentSize();
      if(!b||!['x','y','width','height'].every(k=>Number.isFinite(b[k]))||b.width<1||b.height<1)throw new Error('Invalid desktop view bounds');
      const x=Math.max(0,Math.min(size[0]-1,Math.round(b.x*z))),y=Math.max(0,Math.min(size[1]-1,Math.round(b.y*z)));
      const width=Math.max(1,Math.min(8192,Math.round(b.width*z))),height=Math.max(1,Math.min(8192,Math.round(b.height*z)));
      surface.claim={...body,visible:body.visible===true,blocked:body.blocked!==false};
      if(surface.claim.visible)surface.record.view.setBounds({x,y,width,height});
      this.layout();
      if(!this.interactive(surface.record))await this.stopPicker(surface.record);
      if(surface.revision!==body.revision)return {interactive:this.interactive(surface.record)};
      this.layout();return {interactive:this.interactive(surface.record)};
    });
    handle('detach',async body=>{const surface=this.surfaces.get(body?.surface_id);if(!surface)return;this.surfaces.delete(surface.id);surface.record.surface=null;surface.record.view.setVisible(false);await this.stopPicker(surface.record);this.layout();});
    handle('openDevTools',body=>{const surface=this.surfaces.get(body?.surface_id);if(!surface||!this.interactive(surface.record))throw new Error('请先接管浏览器');surface.record.view.webContents.openDevTools({mode:'detach',activate:true});});
    handle('setPicker',async body=>{
      const surface=this.surfaces.get(body?.surface_id);if(!surface)throw new Error('Unknown desktop surface');
      const record=surface.record;if(!body.enabled)return this.stopPicker(record);
      return this.startPicker(record);
    });
  }
  async close() {
    if(this.closed)return;this.closed=true;clearInterval(this.timer);
    await Promise.allSettled([...this.owners.values()].map(o=>o.runtime?o.runtime.close():this.closeOwner(o)));
    if(!this.shield.webContents.isDestroyed())this.shield.webContents.close({waitForBeforeUnload:false});
  }
}
