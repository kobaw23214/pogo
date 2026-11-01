const $ = (id) => document.getElementById(id);
const state = {
  documents: [], engineUrl: null, fixture: null, documentId: "word", session: null, editor: null,
  bridgeSource: null, bridgeReady: false, editorReady: false, busy: false,
  dirty: false, snapshot: null, status: null, generation: 0, editEpoch: 0, events: [],
  pollTimer: null, inflight: null, queueErrorShown: false, registeringBridge: false,
  closing: false,
  pendingStateReports: 0, stateSyncFailed: false, deferredCommand: null,
  bridgeCapabilities: null,
};

async function api(path, { method = "GET", body, signal } = {}) {
  const response = await fetch(path, {
    method, cache: "no-store", signal,
    ...(body === undefined ? {} : { headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }),
  });
  let data;
  try { data = await response.json(); } catch { throw new Error(`服务返回了无法读取的响应（${response.status}）。`); }
  if (!response.ok || data?.ok === false) throw new Error(typeof data?.error === "string" ? data.error : data?.error?.message || `操作未完成（${response.status}）。`);
  return data;
}

function feedback(message, kind = "idle") {
  $("action-feedback").textContent = message;
  $("action-feedback").dataset.state = kind;
}

function log(message) {
  state.events.unshift({ time: new Date().toLocaleTimeString("zh-CN", { hour12: false }), message });
  state.events.length = Math.min(state.events.length, 30);
  $("event-log").replaceChildren(...state.events.map((entry) => {
    const item = document.createElement("li"); item.textContent = `${entry.time} · ${entry.message}`; return item;
  }));
  $("event-count").textContent = String(state.events.length);
}

function engineState(text, kind = "checking") {
  $("engine-status").querySelector("span").textContent = text;
  $("engine-status").dataset.state = kind;
}

function placeholder(title, copy, retry = false) {
  $("editor-placeholder").hidden = false;
  $("placeholder-title").textContent = title;
  $("placeholder-copy").textContent = copy;
  $("retry-engine").hidden = !retry;
}

function render() {
  const excel = state.documentId === "excel";
  document.querySelectorAll("[data-document]").forEach((button) => {
    button.setAttribute("aria-pressed", String(button.dataset.document === state.documentId));
    button.disabled = state.busy || state.closing || Boolean(state.inflight);
  });
  const current = state.documents.find((item) => item.id === state.documentId);
  $("document-title").textContent = current?.title || (excel ? "Excel 测试文档" : "Word 测试文档");
  $("modify-copy").textContent = excel ? "销售工作表 C2：数量从 3 改为 5，保留公式" : "替换唯一测试段落，保留其他内容";
  $("undo-note").textContent = excel ? "Excel 请在左侧编辑器中按 Ctrl+Z（Mac：⌘Z）撤销；原型尚未提供可验证的程序化撤销接口。" : state.bridgeCapabilities?.nativeRedo === true ? "撤销与重做作用于当前编辑器的整体历史。读取文档可能影响重做历史。" : "Word 原生撤销可用；当前不提供程序化重做。读取文档可能影响编辑器的重做历史。";
  $("bridge-state").textContent = state.bridgeReady ? "同文档插件已连接" : state.editorReady ? "等待同文档插件" : "插件尚未连接";
  const usable = state.bridgeReady && state.editorReady && state.session && !state.busy && !state.closing && !state.inflight;
  $("read-document").disabled = !usable;
  $("modify-document").disabled = !usable || !state.snapshot;
  $("undo-document").disabled = !usable || state.bridgeCapabilities?.nativeUndo !== true;
  $("undo-document").title = "撤销当前编辑器的上一条操作，可能包含用户修改；读取文档可能影响重做历史。";
  $("redo-document").disabled = !usable || state.bridgeCapabilities?.nativeRedo !== true;
  $("redo-document").title = state.bridgeCapabilities?.nativeRedo === true ? "重做当前编辑器的上一条撤销操作。" : "当前引擎未提供可验证的程序化重做；读取文档可能影响编辑器的重做历史。";
  $("save-document").disabled = !usable || Boolean(state.pendingStateReports);
  $("reopen-document").disabled = !state.session || state.busy || state.closing || state.dirty || Boolean(state.inflight) || Boolean(state.pendingStateReports);
  $("reopen-document").title = state.dirty ? "请先保存当前修改，再关闭重开核对文件。" : "销毁当前编辑器，重新读取已保存文件。";
  $("visible-state").dataset.state = state.editorReady ? state.dirty ? "dirty" : "ready" : "offline";
  $("visible-state").lastChild.textContent = state.editorReady ? state.dirty ? "当前画布有修改" : "当前画布已打开" : "画布未连接";
  const saved = state.status?.saveState === "saved";
  const saveError = state.status?.saveState === "error";
  $("persisted-state").dataset.state = saveError ? "error" : saved && !state.dirty ? "saved" : "unknown";
  $("persisted-state").lastChild.textContent = saveError ? "保存失败，修改仍在画布中" : state.status?.saveState === "saving" ? "正在写入测试文件" : saved && !state.dirty ? "文件已持久化保存" : saved ? "已保存旧版本，当前还有修改" : "尚未确认保存";
  $("revision-label").textContent = Number.isFinite(state.status?.revision) ? `版本 ${state.status.revision}` : "版本 —";
}

async function status(generation = state.generation) {
  const editEpoch = state.editEpoch;
  const sessionId = state.session?.id;
  const result = await api(`/api/documents/${state.documentId}/status`);
  if (generation !== state.generation) return;
  state.status = result.status || result;
  if (state.status.activeSessionId === sessionId && state.status.saveState === "saved" && state.editEpoch === editEpoch && !state.pendingStateReports && !state.stateSyncFailed && !state.inflight) state.dirty = false;
  render();
}

async function command(operation, args = {}) {
  if (!state.session || !state.bridgeReady) throw new Error("当前文档的插件尚未连接。");
  const { id, token } = state.session;
  const operationId = crypto.randomUUID();
  const response = await api(`/api/sessions/${encodeURIComponent(id)}/commands`, {
    method: "POST", body: { token, operationId, operation, args },
  });
  return Object.hasOwn(response, "result") ? response.result : response;
}

function showSnapshot(snapshot) {
  state.snapshot = snapshot;
  $("snapshot-output").textContent = JSON.stringify(snapshot, null, 2);
  $("snapshot-label").textContent = `读取于 ${new Date().toLocaleTimeString("zh-CN", { hour12: false })}`;
  render();
}

async function perform(label, action) {
  if (state.busy) return;
  state.busy = true; render(); feedback(`${label}…`, "working");
  try { await action(); log(`${label}完成`); }
  catch (error) { feedback(error.message || "操作未完成，请检查编辑器连接后重试。", "error"); log(`${label}失败：${error.message}`); }
  finally { state.busy = false; render(); }
}

function destroyEditor() {
  unlockExportInput(state.inflight);
  $("editor-save-overlay").hidden = true;
  clearTimeout(state.inflight?.timer);
  clearTimeout(state.pollTimer); state.pollTimer = null; state.inflight = null;
  state.registeringBridge = false; state.queueErrorShown = false;
  state.pendingStateReports = 0; state.stateSyncFailed = false; state.deferredCommand = null;
  state.bridgeCapabilities = null;
  state.bridgeSource = null; state.bridgeReady = false; state.editorReady = false;
  try { state.editor?.destroyEditor(); } catch { /* The new instance still gets a fresh mount. */ }
  state.editor = null;
  const mount = document.createElement("div"); mount.id = "editor"; mount.setAttribute("aria-label", "ONLYOFFICE 在线编辑器");
  document.querySelector(".editor-stage").querySelector("#editor")?.remove();
  document.querySelector(".editor-stage").prepend(mount);
}

async function loadEditorApi(engineUrl) {
  if (window.DocsAPI?.DocEditor) return;
  const script = document.createElement("script");
  script.src = `${engineUrl.replace(/\/$/, "")}/web-apps/apps/api/documents/api.js`;
  script.dataset.officeLab = "engine";
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => { script.remove(); reject(new Error("编辑引擎连接超时，请检查 ONLYOFFICE 服务。")); }, 20000);
    script.onload = () => { clearTimeout(timer); window.DocsAPI?.DocEditor ? resolve() : reject(new Error("服务未提供有效的 ONLYOFFICE Docs API。")); };
    script.onerror = () => { clearTimeout(timer); script.remove(); reject(new Error("无法加载 ONLYOFFICE 编辑引擎，请检查服务地址及浏览器连接。")); };
    document.head.append(script);
  });
}

async function open(documentId = state.documentId) {
  if (state.session) {
    const closing = state.session;
    const previousGeneration = state.generation;
    state.closing = true; render();
    clearTimeout(state.pollTimer); state.pollTimer = null;
    try {
      await api(`/api/sessions/${encodeURIComponent(closing.id)}/close`, { method: "POST", body: { token: closing.token } });
    } catch (error) {
      if (previousGeneration === state.generation && state.session === closing) {
        // A failed close never destroys the visible editor. Resume only after
        // a read-only check confirms the same session is still active.
        try {
          const current = await api(`/api/sessions/${encodeURIComponent(closing.id)}/describe?token=${encodeURIComponent(closing.token)}`);
          if (previousGeneration === state.generation && state.session === closing && current.id === closing.id && current.ready && !current.blocked) {
            state.closing = false;
            if (state.deferredCommand) {
              const deferred = state.deferredCommand; state.deferredCommand = null;
              dispatchCommand(deferred.command, previousGeneration, closing.id);
            } else schedulePoll(previousGeneration, closing.id, 0);
            render();
          }
        } catch { /* Unconfirmed close remains paused; no stale command is replayed. */ }
      }
      throw error;
    }
    if (previousGeneration !== state.generation || state.session !== closing) return;
  }
  const generation = ++state.generation;
  destroyEditor(); state.closing = false; state.documentId = documentId; state.session = null; state.snapshot = null; state.status = null; state.dirty = false;
  $("snapshot-output").textContent = "读取后会在这里显示真实的文档内容。"; $("snapshot-label").textContent = "尚未读取";
  render();
  if (!state.engineUrl) {
    engineState("编辑引擎未配置", "error");
    placeholder("等待 ONLYOFFICE 服务", "尚未连接独立的 ONLYOFFICE Docs 服务。原型不会调用电脑上的 Office，也不会使用模拟编辑器代替真实引擎。", true);
    feedback("配置独立的 ONLYOFFICE Docs 服务后，重新检查即可继续。", "error"); return;
  }
  placeholder("正在打开测试文档", "文档将在当前浏览器中打开，插件随后连接同一份文档。");
  engineState("正在连接编辑引擎");
  try {
    await loadEditorApi(state.engineUrl);
    const opened = await api(`/api/documents/${documentId}/open`, { method: "POST", body: {} });
    if (generation !== state.generation) return;
    state.session = opened.session;
    const config = opened.config;
    config.width = "100%"; config.height = "100%";
    config.events = {
      ...(config.events || {}),
      onDocumentReady: () => {
        if (generation !== state.generation) return;
        state.editorReady = true; $("editor-placeholder").hidden = true;
        engineState("ONLYOFFICE 已连接", "connected"); feedback("可以先在文档中手动修改，再读取当前画布核对。", "idle");
        log("真实 ONLYOFFICE 编辑器已打开"); render();
        if (state.bridgeSource) void registerBridge(state.bridgeSource, state.bridgeCapabilities, generation, state.session.id);
      },
      onDocumentStateChange: (event) => {
        if (generation !== state.generation) return;
        if (event.data === true) { state.dirty = true; state.editEpoch += 1; }
        if (typeof event.data === "boolean") {
          const reportedDirty = event.data;
          state.pendingStateReports += 1;
          void api(`/api/sessions/${encodeURIComponent(state.session.id)}/state`, {
            method: "POST", body: { token: state.session.token, dirty: event.data },
          }).then(() => { if (generation === state.generation && reportedDirty) state.stateSyncFailed = false; })
            .catch(() => { if (generation === state.generation) { if (reportedDirty) state.stateSyncFailed = true; feedback("文档状态同步暂时失败；请保留编辑器，保存时将等待实际文件回执。", "error"); } })
            .finally(() => { if (generation === state.generation) { state.pendingStateReports -= 1; render(); } });
        }
        render();
      },
      onDownloadAs: (event) => {
        if (generation !== state.generation) return;
        void reportExport(event.data, generation);
      },
      onError: (event) => {
        if (generation !== state.generation) return;
        const message = event.data?.errorDescription || "ONLYOFFICE 编辑器报告错误。";
        feedback(message, "error"); engineState("编辑器连接异常", "error"); log(message);
      },
    };
    state.editor = new window.DocsAPI.DocEditor("editor", config);
    await status(generation);
    return true;
  } catch (error) {
    if (generation !== state.generation) return;
    engineState("编辑引擎不可用", "error"); placeholder("文档尚未打开", error.message, true); feedback(error.message, "error"); log(`打开失败：${error.message}`);
    throw error;
  }
}

function matchesSession(generation, sessionId) {
  return generation === state.generation && state.session?.id === sessionId;
}

function schedulePoll(generation, sessionId, delay = 150) {
  clearTimeout(state.pollTimer);
  if (!matchesSession(generation, sessionId) || !state.bridgeReady || state.closing) return;
  state.pollTimer = setTimeout(() => void pollCommands(generation, sessionId), delay);
}

async function pollCommands(generation, sessionId) {
  if (!matchesSession(generation, sessionId) || !state.bridgeReady || state.closing || state.inflight) return;
  const { token } = state.session;
  try {
    const result = await api(`/api/sessions/${encodeURIComponent(sessionId)}/next?token=${encodeURIComponent(token)}`);
    if (!matchesSession(generation, sessionId) || !state.bridgeReady) return;
    if (state.closing) {
      if (result.command) state.deferredCommand = { command: result.command, generation, sessionId };
      return;
    }
    state.queueErrorShown = false;
    if (result.command) {
      dispatchCommand(result.command, generation, sessionId);
      return;
    }
  } catch (error) {
    if (!matchesSession(generation, sessionId)) return;
    if (!state.queueErrorShown) { state.queueErrorShown = true; feedback(`操作队列连接异常：${error.message}`, "error"); log("操作队列暂时不可用，未重复派发文档修改"); }
  }
  schedulePoll(generation, sessionId);
}

function dispatchCommand(command, generation, sessionId) {
  if (!matchesSession(generation, sessionId) || !state.bridgeReady || state.closing || state.inflight) return;
  state.inflight = { generation, sessionId, operationId: command.operationId, operation: command.operation, reporting: false };
  render();
  if (command.operation === "host.export") {
    const active = state.inflight;
    const expectedType = state.documentId === "word" ? "docx" : "xlsx";
    active.fileType = command.args?.fileType;
    active.editEpoch = state.editEpoch;
    lockExportInput(active);
    active.timer = setTimeout(() => {
      if (!matchesSession(generation, sessionId) || state.inflight !== active) return;
      active.expired = true;
      unlockExportInput(active);
      feedback("尚未收到当前编辑器的导出回执，保存结果未确认。请保留文档；不会自动重复导出。", "error");
      log("编辑器导出超时，操作队列已暂停");
    }, 35000);
    try {
      if (active.fileType !== expectedType || typeof state.editor?.downloadAs !== "function") throw new Error("当前编辑器没有提供匹配文档格式的公开导出接口。");
      state.editor.downloadAs(active.fileType);
      log("已请求当前编辑器导出并写回测试文件");
    } catch (error) {
      active.expired = true; clearTimeout(active.timer);
      unlockExportInput(active);
      feedback(`无法确认编辑器导出：${error.message}。请保留文档；不会自动重复执行。`, "error");
      log("编辑器导出未完成，操作队列已暂停");
    }
    return;
  }
  state.bridgeSource.postMessage({ protocol: "pogo-office-lab/v1", type: "request", sessionId, documentId: state.documentId, token: state.session.token, operationId: command.operationId, operation: command.operation, args: command.args || {} }, location.origin);
}

function lockExportInput(active) {
  active.inputElement = $("editor");
  active.previousInert = active.inputElement?.inert ?? false;
  if (active.inputElement) active.inputElement.inert = true;
  $("editor-save-overlay").hidden = false;
}

function unlockExportInput(active) {
  if (active?.inputElement?.isConnected) active.inputElement.inert = active.previousInert;
  if (!active || matchesSession(active.generation, active.sessionId)) $("editor-save-overlay").hidden = true;
}

async function reportExport(data, generation) {
  const active = state.inflight;
  if (!active || active.operation !== "host.export" || active.reporting || active.expired || active.generation !== generation || !matchesSession(generation, active.sessionId)) return;
  if (!data || typeof data.url !== "string" || data.fileType !== active.fileType) {
    active.expired = true; clearTimeout(active.timer);
    unlockExportInput(active);
    feedback("编辑器导出回执的文件地址或格式不匹配，尚未保存。请保留文档核对。", "error");
    log("忽略不匹配的编辑器导出回执"); return;
  }
  active.reporting = true;
  const { token } = state.session;
  try {
    await api(`/api/sessions/${encodeURIComponent(active.sessionId)}/export`, {
      method: "POST", body: { token, operationId: active.operationId, url: data.url, fileType: data.fileType },
    });
    if (!matchesSession(generation, active.sessionId) || state.inflight !== active || active.expired) return;
    unlockExportInput(active);
    clearTimeout(active.timer); state.inflight = null;
    log("编辑器导出文件已由服务写回确认");
    await status(generation);
    if (!matchesSession(generation, active.sessionId)) return;
    render(); schedulePoll(generation, active.sessionId, 0);
  } catch (error) {
    if (!matchesSession(generation, active.sessionId) || state.inflight !== active) return;
    active.expired = true; clearTimeout(active.timer);
    unlockExportInput(active);
    feedback(`导出文件尚未确认写回：${error.message}。请保留文档；不会自动重新导出。`, "error");
    log("导出文件写回未完成，操作队列已暂停");
  }
}

async function registerBridge(source, capabilities, generation, sessionId) {
  if (!matchesSession(generation, sessionId) || !state.editorReady || state.closing || state.registeringBridge || state.bridgeReady) return;
  state.registeringBridge = true;
  const { token } = state.session;
  try {
    await api(`/api/sessions/${encodeURIComponent(sessionId)}/ready`, { method: "POST", body: { token, capabilities: capabilities || {} } });
    if (!matchesSession(generation, sessionId) || source !== state.bridgeSource) return;
    state.bridgeReady = true; render(); log("同文档插件绑定完成"); schedulePoll(generation, sessionId, 0);
  } catch (error) {
    if (matchesSession(generation, sessionId)) feedback(`插件注册未完成：${error.message}`, "error");
  } finally { if (matchesSession(generation, sessionId)) state.registeringBridge = false; }
}

async function reportResponse(message) {
  const active = state.inflight;
  if (!active || active.operation === "host.export" || active.reporting || message.operationId !== active.operationId || !matchesSession(active.generation, active.sessionId)) return;
  active.reporting = true;
  const { token } = state.session;
  const readOnly = active.operation.endsWith(".read") || active.operation === "capabilities";
  if (!readOnly && (message.ok === true || ["OUTCOME_UNKNOWN", "EDITOR_OUTCOME_UNKNOWN"].includes(message.error?.code))) {
    state.dirty = true; state.editEpoch += 1; state.snapshot = null;
    $("snapshot-label").textContent = "文档已变化，请重新读取";
    render();
  }
  try {
    await api(`/api/sessions/${encodeURIComponent(active.sessionId)}/results`, { method: "POST", body: { token, operationId: active.operationId, ok: message.ok === true, result: message.result, error: message.error } });
    if (!matchesSession(active.generation, active.sessionId) || state.inflight !== active) return;
    log(`编辑器回执：${message.ok === true ? "已执行" : "未完成"}`);
    state.inflight = null; render(); schedulePoll(active.generation, active.sessionId, 0);
  } catch (error) {
    if (!matchesSession(active.generation, active.sessionId) || state.inflight !== active) return;
    feedback(`编辑器回执尚未写入服务：${error.message}。请保留当前文档，未自动重复执行。`, "error");
    log("回执传递失败，操作队列已暂停");
  }
}

window.addEventListener("message", (event) => {
  const message = event.data;
  if (event.origin !== location.origin || !event.source || !state.session || !message || message.protocol !== "pogo-office-lab/v1") return;
  if (message.sessionId !== state.session.id || message.documentId !== state.documentId || message.token !== state.session.token) return;
  if (message.type === "ready") {
    if (state.bridgeSource && event.source !== state.bridgeSource) return;
    state.bridgeSource = event.source;
    state.bridgeCapabilities = message.capabilities || {};
    void registerBridge(event.source, message.capabilities, state.generation, state.session.id);
  } else if (message.type === "response" && event.source === state.bridgeSource) {
    void reportResponse(message);
  }
});

$("read-document").addEventListener("click", () => perform("读取当前文档", async () => {
  const snapshot = await command(state.documentId === "word" ? "word.read" : "sheet.read", state.documentId === "word" ? {} : { sheet: "销售", range: "B2:E2" });
  showSnapshot(snapshot); feedback("已从当前编辑器读取。请检查结果是否包含你的未保存修改。");
}));

$("modify-document").addEventListener("click", () => perform("测试修改", async () => {
  const excel = state.documentId === "excel";
  if (!excel && (!state.fixture?.word?.original || !state.fixture.word.edited)) throw new Error("人工测试文档的验证标记尚未配置。");
  if (excel && !Array.isArray(state.snapshot.inputs?.[0])) throw new Error("当前读取结果中缺少表格输入值，请重新读取。");
  const args = excel ? { sheet: "销售", range: "C2", expectedInputs: [[state.snapshot.inputs[0][1]]], inputs: [[5]] } : { search: state.fixture.word.original, replacement: state.fixture.word.edited, expectedText: state.snapshot.text, expectedCount: 1 };
  const result = await command(excel ? "sheet.apply" : "word.replace", args);
  state.dirty = true; state.editEpoch += 1; state.snapshot = null; render();
  $("snapshot-output").textContent = JSON.stringify(result, null, 2); $("snapshot-label").textContent = "修改回执（请重新读取核对）";
  feedback("修改已在当前文档执行。请核对画布，再测试撤销；文件保存仍需单独确认。");
}));

for (const action of ["undo", "redo"]) $( `${action}-document`).addEventListener("click", () => perform(action === "undo" ? "撤销" : "重做", async () => {
  await command(`history.${action}`); state.dirty = true; state.editEpoch += 1; state.snapshot = null; render();
  $("snapshot-label").textContent = "文档已变化，请重新读取";
  feedback(action === "undo" ? "已请求编辑器原生撤销。请核对当前文档并重新读取。" : "已请求编辑器原生重做。请核对当前文档并重新读取。");
}));

$("save-document").addEventListener("click", () => perform("保存测试文件", async () => {
  const { id, token } = state.session;
  const editEpoch = state.editEpoch;
  state.status = { ...state.status, saveState: "saving" }; render();
  await api(`/api/sessions/${encodeURIComponent(id)}/save`, { method: "POST", body: { token } });
  await status();
  if (state.status?.saveState !== "saved") throw new Error("尚未收到编辑器导出并写回的保存确认，请保留当前编辑器并检查服务回执。");
  if (state.editEpoch === editEpoch) state.dirty = false;
  render(); feedback(state.dirty ? "导出写回确认已收到，但保存期间出现了新修改。请再次保存，再关闭重开。" : "编辑器导出文件已写回隔离工作区。现在可以关闭重开，核对保存后的内容。");
}));

$("reopen-document").addEventListener("click", () => perform("关闭重开", async () => {
  if (state.dirty) throw new Error("请先保存当前修改，再关闭重开。");
  if (await open()) feedback("已创建新的编辑器实例。文档打开后请读取，核对最后保存的版本。");
}));

document.querySelectorAll("[data-document]").forEach((button) => button.addEventListener("click", () => {
  if (button.dataset.document === state.documentId) return;
  if (state.dirty) { feedback("当前文档有修改，请先保存，再切换测试文件。", "error"); return; }
  void perform("切换测试文档", () => open(button.dataset.document));
}));

async function bootstrap() {
  return perform("检查编辑引擎", async () => {
    const data = await api("/api/bootstrap");
    state.documents = data.documents || []; state.engineUrl = data.engineUrl || null; state.fixture = data.fixture || null;
    await open();
  });
}

$("retry-engine").addEventListener("click", () => void bootstrap());
window.addEventListener("beforeunload", (event) => { if (state.dirty) { event.preventDefault(); event.returnValue = ""; } });
setInterval(() => { if (state.session && !state.busy) void status().catch(() => {}); }, 5000);
void bootstrap();
