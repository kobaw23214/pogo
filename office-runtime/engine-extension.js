/* Pogo Office History adapter v1. This is an explicit SDK extension, not an
 * upstream ONLYOFFICE API. The upstream native history implementation is kept.
 * Distributed under AGPL-3.0, matching the pinned Community SDK. */
(function () {
  "use strict";
  var adapterId = "pogo-office-history";
  var version = 1;
  var expectedVersion = "9.4.0";
  var expectedBuild = "129";
  function engineMatches(api) { return api["GetVersion"]() === expectedVersion && window.AscCommon.g_cBuildNumber === expectedBuild; }

  function reply(result) { return JSON.stringify({ ok: true, result: result }); }
  function error(code, message) { return JSON.stringify({ ok: false, error: { code: code, message: message } }); }
  function invalid(message) { var failure = new Error(message); failure.code = "INVALID_ARGUMENT"; throw failure; }
  function rangeShape(value) {
    if (typeof value !== "string" || !/^[A-Z]{1,3}[1-9]\d{0,6}(?::[A-Z]{1,3}[1-9]\d{0,6})?$/.test(value)) invalid("A bounded A1 range is required.");
    var cells = value.split(":").map(function (cell) {
      var match = /^([A-Z]+)(\d+)$/.exec(cell), col = 0;
      for (var i = 0; i < match[1].length; i++) col = col * 26 + match[1].charCodeAt(i) - 64;
      return { row: Number(match[2]), col: col };
    });
    var first = cells[0], last = cells[1] || first;
    var rows = last.row - first.row + 1, cols = last.col - first.col + 1;
    if (rows < 1 || cols < 1 || rows * cols > 400 || last.row > 1048576 || last.col > 16384) invalid("The read range must contain at most 400 cells.");
    return { rows: rows, cols: cols };
  }
  function argumentsFor(kind, args) {
    if (!args || typeof args !== "object" || Array.isArray(args)) invalid("Read arguments must be an object.");
    var allowed = kind === "word" ? [] : ["sheet", "range"];
    if (Object.keys(args).some(function (key) { return !allowed.includes(key); })) invalid("Unexpected read arguments.");
    if (kind === "cell") {
      if (typeof args.sheet !== "string" || !args.sheet.length || args.sheet.length > 128) invalid("A worksheet name is required.");
      rangeShape(args.range);
    }
  }
  function historyState(api) {
    return { canUndo: !!api["asc_getCanUndo"](), canRedo: !!api["asc_getCanRedo"]() };
  }
  function mutationArguments(kind, operation, args) {
    if (!args || typeof args !== "object" || Array.isArray(args)) invalid("Mutation arguments must be an object.");
    var allowed = kind === "word" ? ["search", "replacement", "expectedText", "expectedCount"] : ["sheet", "range", "expectedInputs", "inputs"];
    if (operation !== (kind === "word" ? "word.replace" : "sheet.apply") || Object.keys(args).some(function (key) { return !allowed.includes(key); })) invalid("Only a fixed mutation for this editor is allowed.");
    if (kind === "word") {
      if (typeof args.search !== "string" || !args.search.length || args.search.length > 4000 ||
          typeof args.replacement !== "string" || args.replacement.length > 4000 ||
          typeof args.expectedText !== "string" || args.expectedText.length > 200000 || args.expectedCount !== 1 ||
          /[\r\n\t\x00-\x1f^]/.test(args.search + args.replacement)) invalid("Replace exactly one bounded plain-text target.");
      return {};
    }
    var readArgs = { sheet: args.sheet, range: args.range };
    argumentsFor(kind, readArgs);
    var shape = rangeShape(args.range);
    [args.expectedInputs, args.inputs].forEach(function (matrix) {
      if (!Array.isArray(matrix) || matrix.length !== shape.rows || matrix.some(function (row) {
        return !Array.isArray(row) || row.length !== shape.cols || row.some(function (cell) {
          return !["string", "number", "boolean"].includes(typeof cell) || (typeof cell === "number" && !Number.isFinite(cell)) || (typeof cell === "string" && cell.length > 4000);
        });
      })) invalid("A complete bounded input matrix is required.");
    });
    return readArgs;
  }
  function snapshot(api, kind, args) {
    argumentsFor(kind, args);
    // These are the public Office API getters. Unlike callCommand, this fixed
    // plugin method never starts a BuilderScript action or creates a history point.
    var office = api["getJsApi"]();
    if (kind === "word") {
      var doc = office.GetDocument();
      var options = { Numbering: false, ParaSeparator: "\n", NewLineSeparator: "\n", TableRowSeparator: "\n" };
      var text = doc.GetText(options);
      if (text.length > 200000) throw Object.assign(new Error("Word text exceeds 200000 characters."), { code: "DOCUMENT_LIMIT" });
      var selection = doc.GetRangeBySelect();
      return { kind: "word", text: text, selectedText: selection ? selection.GetText(options) : "",
        paragraphs: doc.GetAllParagraphs().map(function (paragraph, index) { return { index: index, text: paragraph.GetText(options) }; }) };
    }
    var sheet = office.GetSheet(args.sheet);
    if (!sheet) throw Object.assign(new Error("The requested worksheet does not exist. Use sheet.list in this document session to read current worksheet names, then read the exact name."), { code: "SHEET_NOT_FOUND" });
    var range = sheet.GetRange(args.range), shape = rangeShape(args.range);
    var values = [], inputs = [], display = [];
    for (var r = 1; r <= shape.rows; r++) {
      var v = [], f = [], d = [];
      for (var c = 1; c <= shape.cols; c++) {
        var cell = range.GetCells(r, c);
        v.push(cell.GetValue()); f.push(cell.GetFormula()); d.push(cell.GetText());
      }
      values.push(v); inputs.push(f); display.push(d);
    }
    return { kind: "cell", sheet: sheet.GetName(), range: args.range, values: values, inputs: inputs, display: display,
      sheets: office.GetSheets().map(function (item) { return item.GetName(); }) };
  }
  function selectionSnapshot(api, kind) {
    // Fixed, synchronous getters on the live canvas only. In particular Word
    // ApiRange.GetText is deliberately avoided: upstream temporarily selects
    // and restores a range. asc_GetSelectedText reads the current selection.
    var options = { Numbering: false, ParaSeparator: "\n", NewLineSeparator: "\n", TableRowSeparator: "\n", TableCellSeparator: "\t" };
    if (kind === "word") {
      if (typeof api["asc_GetSelectedText"] !== "function") throw Object.assign(new Error("The pinned selection getter is unavailable."), { code: "UNSUPPORTED_CAPABILITY" });
      var text = api["asc_GetSelectedText"](false, options);
      if (typeof text !== "string" || !text.trim()) throw Object.assign(new Error("Select some document text before referencing it."), { code: "EMPTY_SELECTION" });
      if (text.length > 32000) throw Object.assign(new Error("Select at most 32000 characters."), { code: "SELECTION_LIMIT" });
      return { kind: "word", text: text, locator: { kind: "text", exact: text } };
    }
    var office = api["getJsApi"](), sheet = office.GetActiveSheet();
    var selected = sheet && sheet.GetSelection();
    if (!selected || selected.GetAreas().GetCount() !== 1) throw Object.assign(new Error("Select one rectangular cell range."), { code: "SELECTION_SHAPE" });
    var address = selected.GetAddress(false, false, "xlA1", false);
    var shape = rangeShape(address), name = sheet.GetName();
    if (typeof name !== "string" || !name.length || name.length > 128) throw Object.assign(new Error("The worksheet name exceeds the supported bounds."), { code: "SELECTION_LIMIT" });
    var values = [], inputs = [], display = [];
    for (var r = 1; r <= shape.rows; r++) {
      var row = [], formulas = [], shown = [];
      for (var c = 1; c <= shape.cols; c++) {
        var cell = selected.GetCells(r, c), value = cell.GetValue(), formula = cell.GetFormula(), visible = cell.GetText();
        if (typeof visible !== "string" || visible.length > 4000) throw Object.assign(new Error("A selected cell exceeds the supported bounds."), { code: "SELECTION_LIMIT" });
        row.push(value); formulas.push(formula); shown.push(visible);
      }
      values.push(row); inputs.push(formulas); display.push(shown);
    }
    var text = display.map(function (row) { return row.join("\t"); }).join("\n");
    if (!text.trim()) throw Object.assign(new Error("The selected cells are empty."), { code: "EMPTY_SELECTION" });
    if (text.length > 32000) throw Object.assign(new Error("The selected cell text exceeds 32000 characters."), { code: "SELECTION_LIMIT" });
    return { kind: "cell", sheet: name, range: address, text: text, values: values, inputs: inputs, display: display };
  }
  function install(kind, constructor) {
    if (typeof constructor !== "function") return false;
    var proto = constructor.prototype;
    var undo = kind === "word" ? "Undo" : "asc_Undo";
    var redo = kind === "word" ? "Redo" : "asc_Redo";
    if (["getJsApi", "GetVersion", "asc_getCanUndo", "asc_getCanRedo", undo, redo,
      "canRunBuilderScript", "_beforeEvalCommand", "_afterEvalCommand", "onEndBuilderScript"].some(function (name) { return typeof proto[name] !== "function"; })) {
      throw new Error("Pogo Office adapter: pinned native SDK entry points were not found.");
    }
    if (proto["pluginMethod_PogoOfficeCapabilities"]) throw new Error("Pogo Office adapter was installed twice.");
    proto["pluginMethod_PogoOfficeCapabilities"] = function () {
      if (!engineMatches(this)) return error("ENGINE_VERSION_MISMATCH", "The Office engine is outside the pinned version.");
      return reply({ id: adapterId, version: version, editor: kind, engineVersion: expectedVersion, engineBuild: expectedBuild,
        sheetList: kind === "cell",
        selectionRead: kind === "cell" || typeof this["asc_GetSelectedText"] === "function",
        readPreservesHistory: true, readPreservesSelection: true, nativeUndo: true, nativeRedo: true, historyWithoutRead: true,
        mutationPreconditionsPreserveHistory: true,
        historyScope: "editor-global", selectiveAgentUndo: false });
    };
    proto["pluginMethod_PogoOfficeSheets"] = function () {
      try {
        if (!engineMatches(this)) return error("ENGINE_VERSION_MISMATCH", "The Office engine is outside the pinned version.");
        if (kind !== "cell") return error("WRONG_EDITOR", "This document is not a spreadsheet.");
        // Public getters only: no BuilderScript transaction, selection change,
        // history entry or disk snapshot. Unsaved renames are observed live.
        var office = this["getJsApi"](), sheets = office.GetSheets();
        if (!Array.isArray(sheets) || sheets.length > 4096) return error("DOCUMENT_LIMIT", "The worksheet list exceeds the supported bounds.");
        var names = sheets.map(function (sheet) { return sheet.GetName(); });
        if (names.some(function (name) { return typeof name !== "string" || !name.length || name.length > 128; })) return error("DOCUMENT_LIMIT", "A worksheet name exceeds the supported bounds.");
        var result = reply({ kind: "cell", sheets: names });
        if (result.length > 200000) return error("DOCUMENT_LIMIT", "The worksheet list exceeds the supported bounds.");
        return result;
      } catch (failure) { return error(failure.code || "EDITOR_API_ERROR", failure.message || "Current worksheet names could not be read."); }
    };
    proto["pluginMethod_PogoOfficeRead"] = function (args) {
      try {
        if (!engineMatches(this)) return error("ENGINE_VERSION_MISMATCH", "The Office engine is outside the pinned version.");
        return reply(snapshot(this, kind, args));
      } catch (failure) { return error(failure.code || "EDITOR_API_ERROR", failure.message || "Current document read failed."); }
    };
    proto["pluginMethod_PogoOfficeSelection"] = function () {
      try {
        if (!engineMatches(this)) return error("ENGINE_VERSION_MISMATCH", "The Office engine is outside the pinned version.");
        var result = reply(selectionSnapshot(this, kind));
        if (result.length > 200000) return error("SELECTION_LIMIT", "The selected content exceeds the supported bounds.");
        return result;
      } catch (failure) { return error(failure.code || "EDITOR_API_ERROR", failure.message || "Current selection could not be read."); }
    };
    proto["pluginMethod_PogoOfficeHistory"] = function (action, args) {
      var executed = false;
      try {
        if (!engineMatches(this)) return error("ENGINE_VERSION_MISMATCH", "The Office engine is outside the pinned version.");
        if (action !== "undo" && action !== "redo") invalid("History accepts only undo or redo.");
        if (!args || typeof args !== "object" || Array.isArray(args)) invalid("History arguments must be an object.");
        var observe = kind !== "cell" || Object.keys(args).length !== 0;
        if (observe) argumentsFor(kind, args);
        var state = historyState(this);
        if (!state[action === "undo" ? "canUndo" : "canRedo"]) return error("NO_HISTORY", "The editor has no corresponding native history action.");
        // Delegate to the same SDK entry point used by the native UI. No
        // beforeimage, compensating SetValue, file replacement or custom history.
        this[action === "undo" ? undo : redo]();
        executed = true;
        var after = historyState(this);
        return reply({ native: true, historyScope: "editor-global", canUndo: after.canUndo, canRedo: after.canRedo,
          snapshot: observe ? snapshot(this, kind, args) : null });
      } catch (failure) {
        return error(executed ? "EDITOR_OUTCOME_UNKNOWN" : failure.code || "EDITOR_API_ERROR", failure.message || "Native history operation failed.");
      }
    };
    proto["pluginMethod_PogoOfficeApply"] = function (operation, args) {
      var api = this, readArgs, before, office;
      try {
        if (!engineMatches(api)) return error("ENGINE_VERSION_MISMATCH", "The Office engine is outside the pinned version.");
        readArgs = mutationArguments(kind, operation, args);
        before = snapshot(api, kind, readArgs);
        office = api["getJsApi"]();
        // This comparison and the transaction start are synchronous in the
        // same editor event. A rejected stale write creates no history point.
        if (kind === "word") {
          if (before.text !== args.expectedText) return error("PRECONDITION_FAILED", "The current document text differs from the text that was read.");
          var matches = office.GetDocument().Search(args.search, true);
          if (!matches || matches.length !== 1) return error("TARGET_NOT_UNIQUE", "The replacement target must match exactly once.");
        } else if (JSON.stringify(before.inputs) !== JSON.stringify(args.expectedInputs)) {
          return error("PRECONDITION_FAILED", "The current cell inputs differ from the inputs that were read.");
        }
      } catch (failure) { return error(failure.code || "EDITOR_API_ERROR", failure.message || "Mutation validation failed."); }

      var manager = window.g_asc_plugins;
      if (!manager || typeof manager.setPluginMethodReturnAsync !== "function" || typeof manager.onPluginMethodReturn !== "function") {
        return error("ENGINE_VERSION_MISMATCH", "The pinned native plugin completion path is missing.");
      }
      manager.setPluginMethodReturnAsync();
      var started = false, ending = false, delivered = false, writeFailure = null;
      function deliver(value) {
        if (delivered) return;
        delivered = true;
        // A synchronous SDK completion must not clear the manager's async flag
        // before its current callMethodInternal stack has returned.
        setTimeout(function () { manager.onPluginMethodReturn(value); }, 0);
      }
      function finish() {
        if (ending) return;
        ending = true; api.evalCommand = false;
        try {
          api.onEndBuilderScript(function (accepted) {
            if (!accepted || writeFailure) {
              deliver(error("EDITOR_OUTCOME_UNKNOWN", writeFailure || "The native transaction did not confirm its result."));
              return;
            }
            try { deliver(reply({ before: before, after: snapshot(api, kind, readArgs) })); }
            catch (failure) { deliver(error("EDITOR_OUTCOME_UNKNOWN", failure.message || "Mutation readback failed.")); }
          });
        } catch (failure) { deliver(error("EDITOR_OUTCOME_UNKNOWN", failure.message || "Native transaction completion failed.")); }
      }
      try {
        if (!api.canRunBuilderScript()) { deliver(error("EDIT_REJECTED", "The current editor cannot begin a mutation transaction.")); return; }
        started = true; api._beforeEvalCommand();
        var changed = kind === "word" ? office.GetDocument().SearchAndReplace({ searchString: args.search, replaceString: args.replacement, matchCase: true }) :
          office.GetSheet(args.sheet).GetRange(args.range).SetValue(args.inputs);
        if (!changed) writeFailure = "The editor did not confirm the requested mutation.";
        // Reuse the original pinned native builder transaction/recalculation;
        // only its placement after the synchronous guard is changed.
        api._afterEvalCommand(finish);
      } catch (failure) {
        if (started) { writeFailure = failure.message || "Native mutation failed."; finish(); }
        else deliver(error("EDITOR_API_ERROR", failure.message || "Native mutation could not start."));
      }
    };
    return true;
  }
  var asc = window.Asc || {};
  var word = install("word", asc["asc_docs_api"]);
  var cell = install("cell", asc["spreadsheet_api"]);
  if (!word && !cell) throw new Error("Pogo Office adapter: no pinned editor SDK was loaded.");
}());
