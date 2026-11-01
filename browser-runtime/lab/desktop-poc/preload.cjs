'use strict';
const { contextBridge, ipcRenderer } = require('electron');

// Loaded only by the trusted shell; fixture WebContentsViews have no preload.
contextBridge.exposeInMainWorld('pogoPocShell', Object.freeze({
  state: () => ipcRenderer.invoke('pogo-poc:state'),
  selectTab: id => ipcRenderer.invoke('pogo-poc:select-tab', id),
  toggleInspect: () => ipcRenderer.invoke('pogo-poc:toggle-inspect'),
  toggleDevtools: () => ipcRenderer.invoke('pogo-poc:toggle-devtools'),
  onState: callback => {
    const listener = (_event, state) => callback(state);
    ipcRenderer.on('pogo-poc:state-changed', listener);
    return () => ipcRenderer.removeListener('pogo-poc:state-changed', listener);
  },
}));
