package workflowcode

// Imports are an ergonomic allow-list in addition to the OS boundary. They are
// not advertised as a Python or V8 security sandbox: introspection can recover
// language internals, and only the operating system confines filesystem,
// network and child-process access.
const pythonWrapper = `import sys, json, builtins, traceback, asyncio, inspect, os
_result = os.fdopen(3, "w", encoding="utf-8", closefd=False)
_payload = json.loads(sys.stdin.buffer.read())
_original_import = builtins.__import__
_allowed = frozenset(("json", "math", "re", "datetime", "decimal", "statistics", "collections", "itertools", "functools", "operator", "string", "base64", "hashlib", "csv", "io", "copy", "enum", "typing", "fractions", "random", "time"))
def _safe_import(name, globals=None, locals=None, fromlist=(), level=0):
    if level or name.split(".")[0] not in _allowed:
        raise ImportError("Module is not available in workflow code: " + name)
    return _original_import(name, globals, locals, fromlist, level)
_builtins = dict(vars(builtins))
_builtins["__import__"] = _safe_import
for _name in ("open", "input", "breakpoint", "help", "exit", "quit"):
    _builtins.pop(_name, None)
_scope = {"__builtins__": _builtins, "__name__": "workflow_code"}
try:
    exec(compile(_payload["source"], "<workflow-code>", "exec"), _scope, _scope)
    _main = _scope.get("main")
    if not callable(_main):
        raise TypeError("Define main(inputs) to return a JSON object")
    _output = _main(_payload["inputs"])
    if inspect.isawaitable(_output):
        _output = asyncio.run(_output)
    if not isinstance(_output, dict):
        raise TypeError("main(inputs) must return a JSON object")
    _frame = json.dumps({"output": _output}, ensure_ascii=False, allow_nan=False, separators=(",", ":"))
    if len(_frame.encode("utf-8")) > 262160:
        raise ValueError("Output exceeds 256KiB")
    _result.write(_frame)
    _result.flush()
except BaseException as _error:
    traceback.print_exc(file=sys.stderr)
    _result.write(json.dumps({"error": type(_error).__name__ + ": " + str(_error)[:1500]}, ensure_ascii=False))
    _result.flush()
    sys.exit(1)
`

const javascriptWrapper = `"use strict";
const fs = require("node:fs");
const vm = require("node:vm");
const util = require("node:util");
const source = JSON.parse(fs.readFileSync(0, "utf8"));
const log = (...args) => process.stdout.write(util.format(...args) + "\n");
const error = (...args) => process.stderr.write(util.format(...args) + "\n");
const context = vm.createContext({console: Object.freeze({log, info:log, debug:log, warn:error, error}), __input_json: JSON.stringify(source.inputs)}, {codeGeneration:{strings:false,wasm:false}});
try {
  const wrapped = '"use strict";\n' + source.source + '\n;if (typeof main !== "function") throw new TypeError("Define main(inputs) to return a JSON object");\nPromise.resolve(main(JSON.parse(__input_json)))';
  Promise.resolve(vm.runInContext(wrapped, context, {filename:"<workflow-code>"})).then(output => {
    if (output === null || typeof output !== "object" || Array.isArray(output)) throw new TypeError("main(inputs) must return a JSON object");
    const seen = new Set();
    const serialized = JSON.stringify({output}, (key, value) => {
      if (typeof value === "number" && !Number.isFinite(value)) throw new TypeError("Output cannot contain NaN or Infinity");
      if (typeof value === "undefined" || typeof value === "function" || typeof value === "symbol" || typeof value === "bigint") throw new TypeError("Output must contain JSON values only");
      return value;
    });
    if (Buffer.byteLength(serialized,"utf8") > 262160) throw new RangeError("Output exceeds 256KiB");
    fs.writeSync(3, serialized);
  }).catch(failed);
} catch (err) { failed(err); }
function failed(err) {
  error(err && err.stack || String(err));
  fs.writeSync(3, JSON.stringify({error:String(err).slice(0,1500)}));
  process.exitCode = 1;
}
`
