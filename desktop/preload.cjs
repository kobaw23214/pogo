'use strict';
const {contextBridge,ipcRenderer}=require('electron');
const invoke=(name,body)=>ipcRenderer.invoke('pogo:desktop-browser:'+name,body);
contextBridge.exposeInMainWorld('pogoDesktopBrowser',Object.freeze({
  version:1,
  attach:body=>invoke('attach',body),update:body=>invoke('update',body),detach:body=>invoke('detach',body),
  openDevTools:body=>invoke('openDevTools',body),setPicker:body=>invoke('setPicker',body),
  onEvent:callback=>{if(typeof callback!=='function')throw new Error('Callback required');const listener=(_event,value)=>callback(value);ipcRenderer.on('pogo:desktop-browser:event',listener);return ()=>ipcRenderer.removeListener('pogo:desktop-browser:event',listener);},
}));
