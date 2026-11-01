'use strict';

// Isolated desktop experiment. Never load Pogo production or a user's profile.
const { app, BrowserWindow, WebContentsView, ipcMain, session } = require('electron');
const fs = require('node:fs');
const path = require('node:path');

const dataDir = process.env.POGO_POC_DATA_DIR;
if (!dataDir || !path.isAbsolute(dataDir)) throw new Error('POGO_POC_DATA_DIR must be an isolated absolute path');
const originURL = new URL(process.env.POGO_POC_ORIGIN || '');
if (originURL.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(originURL.hostname) ||
    originURL.username || originURL.password || originURL.pathname !== '/' || originURL.search || originURL.hash) {
  throw new Error('POGO_POC_ORIGIN must be a loopback HTTP origin');
}
const origin = originURL.origin;
fs.mkdirSync(dataDir, { recursive: true });
fs.mkdirSync(path.join(dataDir, 'session-data'), { recursive: true });
app.setPath('userData', dataDir);
app.setPath('sessionData', path.join(dataDir, 'session-data'));
app.setName('Pogo Desktop Browser PoC');

const toolbarHeight = 72;
const highlightConfig = {
  showInfo: true, showStyles: false,
  contentColor: { r: 69, g: 140, b: 235, a: 0.22 },
  borderColor: { r: 69, g: 140, b: 235, a: 0.75 },
};
const tabs = new Map();
let win, activeTab = 'A', shuttingDown = false;
const allowedURL = value => {
  try { const url = new URL(value); return url.origin === origin && !url.username && !url.password; }
  catch { return false; }
};
const errorText = error => String(error?.message || error).slice(0, 240);
const getTab = id => {
  const tab = tabs.get(id);
  if (!tab || tab.view.webContents.isDestroyed()) throw new Error('Unknown or closed PoC tab');
  return tab;
};
const selected = tab => tab.inspect.selected ? { ...tab.inspect.selected } : null;
function snapshotInspectState() {
  return { activeTab, tabs: [...tabs.values()].map(tab => ({
    id: tab.id, enabled: tab.inspect.enabled, documentEpoch: tab.documentEpoch,
    selected: selected(tab), lastError: tab.inspect.lastError,
  })) };
}
function shellState() {
  return { ...snapshotInspectState(), tabs: [...tabs.values()].map(tab => ({
    id: tab.id, enabled: tab.inspect.enabled, selected: selected(tab),
    lastError: tab.inspect.lastError, devtools: !tab.view.webContents.isDestroyed() && tab.view.webContents.isDevToolsOpened(),
  })) };
}
function publish() {
  if (win && !win.isDestroyed() && !win.webContents.isDestroyed()) win.webContents.send('pogo-poc:state-changed', shellState());
}
function layout() {
  if (!win || win.isDestroyed()) return;
  const [width, height] = win.getContentSize();
  for (const tab of tabs.values()) {
    tab.view.setBounds({ x: 0, y: toolbarHeight, width, height: Math.max(1, height - toolbarHeight) });
    tab.view.setVisible(tab.id === activeTab);
  }
}

async function setInspect(id, enabled) {
  const tab = getTab(id), debuggerAPI = tab.view.webContents.debugger;
  tab.inspect.lastError = null;
  try {
    if (enabled) {
      if (!debuggerAPI.isAttached()) debuggerAPI.attach('1.3');
      await debuggerAPI.sendCommand('DOM.enable');
      await debuggerAPI.sendCommand('Overlay.enable');
      await debuggerAPI.sendCommand('Overlay.setInspectMode', {
        mode: 'searchForNode',
        highlightConfig,
      });
      tab.inspect.selected = null;
      tab.inspect.enabled = true;
    } else {
      if (debuggerAPI.isAttached()) await debuggerAPI.sendCommand('Overlay.setInspectMode', { mode: 'none', highlightConfig });
      tab.inspect.enabled = false;
    }
  } catch (error) {
    tab.inspect.enabled = false;
    tab.inspect.lastError = errorText(error);
    publish();
    throw error;
  }
  publish();
  return snapshotInspectState();
}

async function selectTab(id) {
  const next = getTab(id), previous = tabs.get(activeTab);
  if (previous && previous.id !== id && previous.inspect.enabled) await setInspect(previous.id, false);
  activeTab = next.id;
  layout();
  next.view.webContents.focus();
  publish();
  return { activeTab };
}

async function openDevtools(id = activeTab) {
  const contents = getTab(id).view.webContents;
  if (!contents.isDevToolsOpened()) await contentsEvent(contents, 'devtools-opened', () => contents.openDevTools({ mode: 'detach', activate: true }));
  return { id, opened: contents.isDevToolsOpened() };
}
async function closeDevtools(id = activeTab) {
  const contents = getTab(id).view.webContents;
  if (contents.isDevToolsOpened()) await contentsEvent(contents, 'devtools-closed', () => contents.closeDevTools());
  return { id, opened: contents.isDevToolsOpened() };
}
function contentsEvent(contents, name, action) {
  return new Promise((resolve, reject) => {
    let timer;
    const finish = error => {
      clearTimeout(timer); contents.removeListener(name, done); contents.removeListener('destroyed', destroyed);
      error ? reject(error) : resolve();
    };
    const done = () => finish(), destroyed = () => finish(new Error('PoC tab closed during DevTools transition'));
    contents.once(name, done); contents.once('destroyed', destroyed);
    timer = setTimeout(() => finish(new Error('PoC DevTools transition timed out')), 4000);
    try { action(); } catch (error) { finish(error); }
  });
}

function registerTab(id, profile) {
  const view = new WebContentsView({ webPreferences: {
    session: profile, nodeIntegration: false, contextIsolation: true, sandbox: true,
  } });
  const contents = view.webContents;
  const tab = { id, view, documentEpoch: 0, inspect: { enabled: false, selected: null, lastError: null } };
  tabs.set(id, tab);
  win.contentView.addChildView(view);
  contents.setWindowOpenHandler(() => ({ action: 'deny' }));
  contents.on('will-navigate', (event, url) => { if (!allowedURL(url)) event.preventDefault(); });
  contents.on('will-frame-navigate', (event, details) => {
    // Electron's newer event shape places navigation details on the event;
    // accept the explicit details form too for straightforward version tests.
    const url = details?.url ?? event.url;
    if (url && !allowedURL(url)) event.preventDefault();
  });
  contents.on('will-redirect', (event, url) => { if (!allowedURL(url)) event.preventDefault(); });
  contents.on('did-navigate', () => {
    tab.documentEpoch++;
    tab.inspect.enabled = false;
    tab.inspect.selected = null;
    publish();
  });
  contents.on('devtools-opened', publish);
  contents.on('devtools-closed', publish);
  contents.debugger.on('detach', (_event, reason) => {
    tab.inspect.enabled = false;
    tab.inspect.lastError = shuttingDown ? null : `Debugger detached: ${String(reason).slice(0, 180)}`;
    publish();
  });
  contents.debugger.on('message', (_event, method, params, sessionId) => {
    if (method !== 'Overlay.inspectNodeRequested' || !tab.inspect.enabled || tab.inspect.selected || !Number.isInteger(params.backendNodeId)) return;
    tab.inspect.selected = { backendNodeId: params.backendNodeId, sessionId: sessionId || '', documentEpoch: tab.documentEpoch, selectedAt: Date.now() };
    contents.debugger.sendCommand('Overlay.setInspectMode', { mode: 'none', highlightConfig }).catch(error => {
      tab.inspect.lastError = errorText(error);
    }).finally(() => {
      tab.inspect.enabled = false;
      publish();
    });
    publish();
  });
  return tab;
}

app.whenReady().then(async () => {
  const profile = session.fromPartition('persist:pogo-desktop-poc');
  profile.setPermissionRequestHandler((_contents, _permission, callback) => callback(false));
  profile.setPermissionCheckHandler(() => false);
  profile.webRequest.onBeforeRequest((details, callback) => {
    // The website session may visit only the harness origin. DevTools' own
    // built-in resources are allowed; target navigations remain origin-bound.
    const internal = details.url === 'about:blank' || details.url.startsWith('devtools://');
    callback({ cancel: !internal && !allowedURL(details.url) });
  });
  win = new BrowserWindow({
    title: 'Pogo Desktop Browser PoC', width: 1280, height: 900,
    minWidth: 640, minHeight: 400, backgroundColor: '#f4f6f8',
    webPreferences: { preload: path.join(__dirname, 'preload.cjs'), nodeIntegration: false, contextIsolation: true, sandbox: true },
  });
  win.removeMenu();
  win.on('page-title-updated', event => event.preventDefault());
  const authorizeShell = event => {
    if (win?.isDestroyed() || event.sender !== win?.webContents || event.senderFrame !== win.webContents.mainFrame) throw new Error('PoC shell only');
  };
  ipcMain.handle('pogo-poc:state', event => { authorizeShell(event); return shellState(); });
  ipcMain.handle('pogo-poc:select-tab', (event, id) => { authorizeShell(event); return selectTab(id); });
  ipcMain.handle('pogo-poc:toggle-inspect', event => { authorizeShell(event); return setInspect(activeTab, !getTab(activeTab).inspect.enabled); });
  ipcMain.handle('pogo-poc:toggle-devtools', event => {
    authorizeShell(event);
    return getTab(activeTab).view.webContents.isDevToolsOpened() ? closeDevtools() : openDevtools();
  });
  win.on('resize', layout);
  win.on('closed', () => {
    shuttingDown = true;
    for (const tab of tabs.values()) {
      const contents = tab.view.webContents;
      if (!contents.isDestroyed()) contents.close({ waitForBeforeUnload: false });
    }
    tabs.clear();
    app.quit();
  });
  for (const id of ['A', 'B']) registerTab(id, profile);
  layout();
  global.__pogoPoc = Object.freeze({
    listTabs: () => [...tabs.values()].map(tab => ({ id: tab.id, webContentsId: tab.view.webContents.id, url: tab.view.webContents.getURL() })),
    selectTab, getMetrics: () => app.getAppMetrics(),
    getBounds: () => ({ activeTab, toolbarHeight, window: win.getBounds(), views: [...tabs.values()].map(tab => ({
      id: tab.id, webContentsId: tab.view.webContents.id, bounds: tab.view.getBounds(), visible: tab.view.getVisible(),
    })) }),
    setInspect, openDevtools, closeDevtools, snapshotInspectState,
  });
  await win.loadFile(path.join(__dirname, 'shell.html'));
  await Promise.all([...tabs.values()].map(tab => tab.view.webContents.loadURL(`${origin}/fixture.html?tab=${tab.id}`)));
  getTab(activeTab).view.webContents.focus();
  publish();
}).catch(error => {
  console.error(`Pogo desktop PoC startup failed: ${errorText(error)}`);
  app.exit(1);
});

app.on('window-all-closed', () => app.quit());
