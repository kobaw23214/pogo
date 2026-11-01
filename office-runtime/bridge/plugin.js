/* Pogo's typed bridge requires the explicit, pinned Office runtime extension. */
(function () {
  "use strict";

  var protocol = "pogo-office-lab/v1";
  var identityFields = ["sessionId", "documentId", "token", "editor"];
  var identity = window.__pogoOfficeLabScope;
  if (identity !== undefined) {
    // The host validates the path's session/token before serving this script.
    // Loader-added lang/theme parameters cannot alter that scoped identity.
    if (!identity || typeof identity !== "object" || Array.isArray(identity) ||
        Object.keys(identity).some(function (key) { return !identityFields.includes(key); }) ||
        identityFields.some(function (key) { return typeof identity[key] !== "string"; })) return;
  } else {
    // Legacy test URLs only: never strip pollution or mix identity sources.
    var query = new URLSearchParams(location.search || "");
    var hasQueryScope = identityFields.some(function (key) { return query.has(key); });
    var legacyScope = hasQueryScope ? query : new URLSearchParams(location.hash.slice(1));
    identity = {};
    identityFields.forEach(function (key) { identity[key] = legacyScope.get(key); });
  }
  var sessionId = identity.sessionId;
  var documentId = identity.documentId;
  var token = identity.token;
  var editor = identity.editor;
  var plugin = window.Asc && window.Asc.plugin;
  var results = new Map();
  var nativePending = new Set();
  var unknownWrites = new Set();
  var reconciling = false;
  var inFlight = null;
  var blocked = false;
  var ready = false;
  var engineAdapter = null;
  var lastSheetRead = null;
  var limit = 512;
  var timeoutMs = 30000;
  // Bootstrap is read-only and has a separate cold-start budget. Native edit
  // deadlines and their original receipts/reconciliation remain unchanged.
  var bootstrapTimeoutMs = 300000;
  var readyRetryMs = 5000;
  var readyTimer;
  var readyAcknowledged = false;
  var readyCapabilities = null;
  var bootstrapRecord = null;
  var bootstrapGeneration = 0;
  var bootstrapError = null;
  var disposed = false;

  if (!plugin || window.top === window || typeof sessionId !== "string" || !sessionId ||
      typeof documentId !== "string" || !documentId || typeof token !== "string" ||
      token.length < 32 || !["word", "cell"].includes(editor)) return;

  function post(type, fields) {
    window.top.postMessage(Object.assign({
      protocol: protocol, type: type, sessionId: sessionId,
      documentId: documentId, token: token
    }, fields), location.origin);
  }

  function failure(code, message) {
    var error = new Error(message);
    error.code = code;
    return error;
  }

  function capabilities() {
    return {
      editor: editor,
      read: !!engineAdapter,
      wordReplace: !!engineAdapter && editor === "word",
      sheetApply: !!engineAdapter && editor === "cell",
      sheetList: !!engineAdapter && editor === "cell" && engineAdapter.sheetList === true,
      selectionRead: !!engineAdapter && engineAdapter.selectionRead === true,
      nativeUndo: !!engineAdapter,
      nativeRedo: !!engineAdapter,
      historyWithoutRead: !!engineAdapter,
      readPreservesHistory: !!engineAdapter,
      readPreservesSelection: !!engineAdapter,
      mutationPreconditionsPreserveHistory: !!engineAdapter,
      engineExtension: { id: "pogo-office-history", version: 1, required: true, detected: !!engineAdapter,
        engineVersion: engineAdapter && engineAdapter.engineVersion, engineBuild: engineAdapter && engineAdapter.engineBuild },
      selectiveAgentUndo: false,
      historyScope: "editor-global",
      groupedAcrossCommands: false,
      save: "host-download-as",
      deduplication: "this-plugin-instance-only",
      blocked: blocked
    };
  }

  function sendReady() {
    if (!ready || disposed) return;
    clearTimeout(readyTimer);
    // Arm before posting: a synchronous synthetic host can acknowledge the
    // message immediately, and that acknowledgement must clear this timer too.
    if (!readyAcknowledged) {
      readyTimer = setTimeout(sendReady, readyRetryMs);
      if (readyTimer && typeof readyTimer.unref === "function") readyTimer.unref();
    }
    post("ready", { capabilities: readyCapabilities });
  }

  function announceReady() {
    ready = true; bootstrapError = null; readyAcknowledged = false;
    readyCapabilities = capabilities();
    sendReady();
  }

  function announceBootstrapError(error) {
    bootstrapError = { recoverable: true, error: { code: error.code || "ENGINE_BOOTSTRAP_FAILED", message: error.message } };
    post("bootstrap-error", bootstrapError);
  }

  function beginBootstrap(explicitRetry) {
    if (disposed) return Promise.resolve();
    if (ready && (!explicitRetry || engineAdapter)) {
      readyAcknowledged = false; sendReady(); return Promise.resolve();
    }
    if (bootstrapRecord && !bootstrapRecord.settled && !bootstrapRecord.expired) return bootstrapRecord.wait;
    var generation = ++bootstrapGeneration;
    var record = { settled: false, expired: false, timer: undefined, wait: null };
    bootstrapRecord = record;
    ready = false; bootstrapError = null; readyAcknowledged = false; clearTimeout(readyTimer);
    record.wait = new Promise(function (resolve) {
      record.timer = setTimeout(function () {
        if (record.settled || disposed || generation !== bootstrapGeneration) return;
        record.expired = true;
        // No callback is not evidence that the adapter is unsupported. Retain
        // the original getter and accept a late success in this same canvas.
        announceBootstrapError(failure("ENGINE_BOOTSTRAP_TIMEOUT", "The Office connection is still waiting for its native adapter. Retry this connection; the document canvas and manual input are preserved."));
        resolve();
      }, bootstrapTimeoutMs);
      if (record.timer && typeof record.timer.unref === "function") record.timer.unref();
      Promise.resolve().then(function () { return adapterMethod("PogoOfficeCapabilities", []); }).then(function (detected) {
        if (disposed || generation !== bootstrapGeneration) return;
        engineAdapter = detected && detected.id === "pogo-office-history" && detected.version === 1 && detected.editor === editor &&
          detected.readPreservesHistory === true && detected.readPreservesSelection === true &&
          detected.nativeUndo === true && detected.nativeRedo === true && detected.historyWithoutRead === true &&
          detected.mutationPreconditionsPreserveHistory === true ? detected : null;
        announceReady();
      }, function () {
        // An actual, settled unsupported/invalid adapter response retains the
        // previous explicit capability=false contract. A timeout never does.
        if (disposed || generation !== bootstrapGeneration) return;
        engineAdapter = null; announceReady();
      }).finally(function () {
        record.settled = true; clearTimeout(record.timer); resolve();
      });
    });
    return record.wait;
  }

  function assertPlain(args, names) {
    if (!args || typeof args !== "object" || Array.isArray(args) ||
        Object.getPrototypeOf(args) !== Object.prototype ||
        Object.keys(args).some(function (key) { return !names.includes(key); })) {
      throw failure("INVALID_ARGUMENT", "Unexpected operation arguments.");
    }
  }

  function assertText(value, max, allowEmpty) {
    if (typeof value !== "string" || value.length > max || (!allowEmpty && !value.length)) {
      throw failure("INVALID_ARGUMENT", "Text argument exceeds the prototype's bounds.");
    }
  }

  function assertRange(value) {
    if (typeof value !== "string" || !/^[A-Z]{1,3}[1-9]\d{0,6}(?::[A-Z]{1,3}[1-9]\d{0,6})?$/.test(value)) {
      throw failure("INVALID_ARGUMENT", "Use a single bounded A1 range.");
    }
    var cells = value.split(":").map(function (cell) {
      var match = /^([A-Z]+)(\d+)$/.exec(cell);
      var col = 0;
      for (var i = 0; i < match[1].length; i++) col = col * 26 + match[1].charCodeAt(i) - 64;
      return { col: col, row: Number(match[2]) };
    });
    var start = cells[0], end = cells[1] || start;
    var rows = end.row - start.row + 1, cols = end.col - start.col + 1;
    if (rows < 1 || cols < 1 || rows * cols > 400 || end.row > 1048576 || end.col > 16384) {
      throw failure("INVALID_ARGUMENT", "The prototype accepts at most 400 cells in one range.");
    }
    return { rows: rows, cols: cols };
  }

  function assertMatrix(value, size) {
    if (!Array.isArray(value) || value.length !== size.rows || value.some(function (row) {
      return !Array.isArray(row) || row.length !== size.cols || row.some(function (cell) {
        return !["string", "number", "boolean"].includes(typeof cell) ||
          (typeof cell === "number" && !Number.isFinite(cell)) ||
          (typeof cell === "string" && cell.length > 4000);
      });
    })) throw failure("INVALID_ARGUMENT", "Cell inputs must be a complete rectangular matrix.");
  }

  function validate(operation, args) {
    if (operation === "selection.read") {
      assertPlain(args, ["documentId", "instanceId", "canvasVersion", "dirtyEpoch"]);
      if (args.documentId !== documentId || typeof args.instanceId !== "string" || !/^[A-Za-z0-9_-]{8,100}$/.test(args.instanceId) ||
          typeof args.canvasVersion !== "string" || !/^[a-f0-9]{64}$/.test(args.canvasVersion) || !Number.isSafeInteger(args.dirtyEpoch) || args.dirtyEpoch < 0) throw failure("INVALID_ARGUMENT", "The selection binding is invalid.");
      if (!engineAdapter || engineAdapter.selectionRead !== true) throw failure("UNSUPPORTED_CAPABILITY", "This Office engine does not advertise live selection reading.");
    } else if (operation === "word.read") {
      assertPlain(args, []);
      if (editor !== "word") throw failure("WRONG_EDITOR", "This document is not a Word document.");
    } else if (operation === "word.replace") {
      assertPlain(args, ["search", "replacement", "expectedText", "expectedCount"]);
      if (editor !== "word") throw failure("WRONG_EDITOR", "This document is not a Word document.");
      assertText(args.search, 4000, false);
      assertText(args.replacement, 4000, true);
      assertText(args.expectedText, 200000, true);
      if (args.expectedCount !== 1 || /[\r\n\t\x00-\x1f^]/.test(args.search + args.replacement)) {
        throw failure("INVALID_ARGUMENT", "Replace one unique plain-text span without search control sequences.");
      }
    } else if (operation === "sheet.read" || operation === "sheet.apply") {
      assertPlain(args, operation === "sheet.read" ? ["sheet", "range"] :
        ["sheet", "range", "expectedInputs", "inputs"]);
      if (editor !== "cell") throw failure("WRONG_EDITOR", "This document is not a spreadsheet.");
      assertText(args.sheet, 128, false);
      var size = assertRange(args.range);
      if (operation === "sheet.apply") {
        assertMatrix(args.expectedInputs, size);
        assertMatrix(args.inputs, size);
      }
    } else if (operation === "sheet.list") {
      assertPlain(args, []);
      if (editor !== "cell") throw failure("WRONG_EDITOR", "This document is not a spreadsheet.");
      if (!engineAdapter || engineAdapter.sheetList !== true) throw failure("UNSUPPORTED_CAPABILITY", "The current Office engine does not advertise live worksheet discovery.");
    } else if (operation === "history.undo" || operation === "history.redo" || operation === "capabilities") {
      assertPlain(args, editor === "cell" && operation !== "capabilities" ? ["sheet", "range"] : []);
      if (editor === "cell" && operation !== "capabilities" && Object.keys(args).length) {
        assertText(args.sheet, 128, false); assertRange(args.range);
      }
    } else throw failure("UNSUPPORTED_OPERATION", "The bridge does not execute arbitrary scripts.");
  }

  function method(name, args) {
    return new Promise(function (resolve) { plugin.executeMethod(name, args || [], resolve); });
  }

  async function adapterMethod(name, args) {
    var value = await method(name, args);
    if (typeof value !== "string") throw failure("EDITOR_OUTCOME_UNKNOWN", "The required native Office adapter returned no verifiable result.");
    var envelope;
    try { envelope = JSON.parse(value); } catch (_) { throw failure("EDITOR_OUTCOME_UNKNOWN", "The native Office adapter returned an invalid result."); }
    if (!envelope || typeof envelope.ok !== "boolean") throw failure("EDITOR_OUTCOME_UNKNOWN", "The native Office adapter returned an invalid result.");
    if (!envelope.ok) throw failure(envelope.error.code, envelope.error.message);
    return envelope.result;
  }

  async function execute(operation, args) {
    if (operation === "capabilities") return capabilities();
    if (!engineAdapter) throw failure("ENGINE_EXTENSION_REQUIRED", "This integration requires the pinned Pogo Office history adapter. Unmodified Community engines are not supported by this path.");
    if (operation === "sheet.list") return adapterMethod("PogoOfficeSheets", []);
    if (operation === "selection.read") return adapterMethod("PogoOfficeSelection", []);
    if (operation === "word.read" || operation === "sheet.read") {
      var observed = await adapterMethod("PogoOfficeRead", [args]);
      if (operation === "sheet.read") lastSheetRead = { sheet: args.sheet, range: args.range };
      return observed;
    }
    if (operation === "history.undo" || operation === "history.redo") {
      // Global native history does not require a prior Agent read. When no
      // observation scope exists, the extension returns snapshot:null honestly.
      var readArgs = editor === "word" ? {} : Object.keys(args).length ? args : lastSheetRead || {};
      return adapterMethod("PogoOfficeHistory", [operation === "history.undo" ? "undo" : "redo", readArgs]);
    }
    var result = await adapterMethod("PogoOfficeApply", [operation, args]);
    if (operation === "sheet.apply") lastSheetRead = { sheet: args.sheet, range: args.range };
    return result;
  }

  function isRead(operation) {
    return operation === "word.read" || operation === "sheet.read" || operation === "sheet.list" || operation === "selection.read" || operation === "capabilities";
  }

  function pendingMutation() {
    return Array.from(nativePending).some(function (record) { return !isRead(record.operation); });
  }

  function receipt(id, operation, outcome, error) {
    if (!error) return { operationId: id, ok: true, result: outcome };
    var code = error.code || "BRIDGE_ERROR";
    if (isRead(operation) && (code === "EDITOR_OUTCOME_UNKNOWN" || code === "OUTCOME_UNKNOWN")) code = "READ_FAILED";
    return { operationId: id, ok: false, error: { code: code, message: error.message || "The editor operation failed." } };
  }

  // Recovery observes the original native promises; it never executes a write
  // again. A real native getter also fences the editor queue before confirming
  // settled work. Unverifiable native results remain explicit error receipts.
  async function reconcile(request) {
    if (typeof request.barrierId !== "string" || !/^[A-Za-z0-9_-]{8,100}$/.test(request.barrierId) ||
        !Number.isSafeInteger(request.recoveryRevision) || request.recoveryRevision < 1 ||
        !Array.isArray(request.operationIds) || request.operationIds.length > 8 ||
        new Set(request.operationIds).size !== request.operationIds.length ||
        request.operationIds.some(function (id) { return typeof id !== "string" || !/^[A-Za-z0-9_-]{8,100}$/.test(id); })) return;
    var fields = { barrierId: request.barrierId, recoveryRevision: request.recoveryRevision, settled: false, operations: [] };
    var records = request.operationIds.map(function (id) { return results.get(id); });
    if (reconciling || pendingMutation() || records.some(function (record) { return !record || isRead(record.operation) || !record.nativeSettled || !record.nativeResponse; })) {
      post("reconciled", fields); return;
    }
    reconciling = true;
    var timer;
    try {
      var checked = await Promise.race([
        adapterMethod("PogoOfficeCapabilities", []),
        new Promise(function (_, reject) { timer = setTimeout(function () { reject(failure("READ_TIMEOUT", "The native recovery barrier has not settled.")); }, Math.min(timeoutMs, 5000)); })
      ]);
      if (!checked || checked.id !== "pogo-office-history" || checked.version !== 1 || checked.editor !== editor || checked.historyWithoutRead !== true ||
          pendingMutation() || records.some(function (record) { return !record.nativeSettled; })) {
        post("reconciled", fields); return;
      }
      fields.settled = true;
      fields.operations = records.map(function (record) {
        return Object.assign({ operation: record.operation }, record.nativeResponse);
      });
      request.operationIds.forEach(function (id) { unknownWrites.delete(id); });
      blocked = unknownWrites.size > 0;
      post("reconciled", fields);
    } catch (_) { post("reconciled", fields); }
    finally { clearTimeout(timer); reconciling = false; }
  }

  window.addEventListener("message", async function (event) {
    var request = event.data;
    if (event.source !== window.top || event.origin !== location.origin || disposed ||
        !request || request.protocol !== protocol ||
        request.token !== token || request.sessionId !== sessionId || request.documentId !== documentId) return;
    if (request.type === "ready-ack") {
      if (ready) { readyAcknowledged = true; clearTimeout(readyTimer); }
      return;
    }
    if (request.type === "hello" || request.type === "retry-bootstrap") {
      if (ready && (request.type === "hello" || engineAdapter)) {
        readyAcknowledged = false; sendReady();
      } else if (request.type === "retry-bootstrap") {
        await beginBootstrap(true);
      } else if (bootstrapError) post("bootstrap-error", bootstrapError);
      return;
    }
    if (!ready) return;
    if (request.type === "reconcile") { await reconcile(request); return; }
    if (request.type !== "request" || typeof request.operationId !== "string" || !/^[A-Za-z0-9_-]{8,100}$/.test(request.operationId)) return;
    var id = request.operationId;
    var fingerprint;
    try { fingerprint = JSON.stringify([request.operation, request.args]); }
    catch (_) { return; }
    var previous = results.get(id);
    if (previous) {
      post("response", previous.fingerprint === fingerprint ? previous.response || {
        operationId: id, ok: false, error: { code: "OPERATION_PENDING", message: "The original native operation is still pending." }
      } : {
        operationId: id, ok: false, error: { code: "OPERATION_ID_CONFLICT", message: "An operation ID was reused with a different request." }
      });
      return;
    }
    if (inFlight) {
      post("response", { operationId: id, ok: false, error: {
        code: inFlight.id === id ? (inFlight.fingerprint === fingerprint ? "OPERATION_PENDING" : "OPERATION_ID_CONFLICT") : "BUSY",
        message: "The previous editor operation is still pending; do not replay a write."
      } });
      return;
    }
    if (results.size >= limit || (blocked && !isRead(request.operation))) {
      post("response", { operationId: id, ok: false, error: {
        code: blocked ? "SESSION_BLOCKED" : "SESSION_LIMIT", message: blocked ?
          "Recovery is checking the original native operation; read-only commands remain available." :
          "The editor receipt limit is full. Save the shared document before opening a new editor session."
      } });
      return;
    }
    var response, record, timer;
    try {
      validate(request.operation, request.args);
      record = { id: id, fingerprint: fingerprint, operation: request.operation, nativeSettled: false, nativeResponse: null, response: null };
      results.set(id, record);
      inFlight = record;
      nativePending.add(record);
      var actual = Promise.resolve().then(function () { return execute(request.operation, request.args); }).then(function (outcome) {
        return receipt(id, request.operation, outcome, null);
      }, function (error) {
        return receipt(id, request.operation, null, error);
      }).then(function (value) {
        record.nativeSettled = true; record.nativeResponse = value;
        nativePending.delete(record);
        return value;
      });
      var outcome = await Promise.race([
        actual,
        new Promise(function (_, reject) { timer = setTimeout(function () {
          reject(failure(isRead(request.operation) ? "READ_TIMEOUT" : "EDITOR_OUTCOME_UNKNOWN", isRead(request.operation) ?
            "The editor read timed out. Reconnect and read again; no write was replayed." :
            "The editor operation timed out. Recovery will check the original execution before another edit."));
        }, timeoutMs); })
      ]).finally(function () { clearTimeout(timer); });
      response = outcome;
    } catch (error) {
      response = receipt(id, request.operation, null, error);
    } finally { clearTimeout(timer); if (!record || inFlight === record) inFlight = null; }
    if (!isRead(request.operation) && response.error && ["EDITOR_OUTCOME_UNKNOWN", "OUTCOME_UNKNOWN"].includes(response.error.code)) {
      unknownWrites.add(id); blocked = true;
    }
    if (record) record.response = response;
    else results.set(id, { fingerprint: fingerprint, operation: request.operation, response: response, nativeSettled: true, nativeResponse: response });
    post("response", response);
  });

  plugin.init = async function () {
    if (plugin.info && plugin.info.editorType && plugin.info.editorType !== editor) return;
    await beginBootstrap(false);
  };
  window.addEventListener("pagehide", function () {
    disposed = true; clearTimeout(readyTimer); clearTimeout(bootstrapRecord && bootstrapRecord.timer);
  });
  plugin.button = function () {};
}());
