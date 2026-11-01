// Executed before upstream scripts. Only this registered origin is remapped;
// external destinations and other Pogo routes are never guessed from Referer.
(() => {
  "use strict";
  const config = __POGO_PANEL_CONFIG__;
  const mounted = path => path === config.prefix || path.startsWith(config.prefix + "/");
  const upstreamBase = () => {
    const current = new URL(document.baseURI || location.href);
    if (current.origin === location.origin && mounted(current.pathname)) {
      return config.source + current.pathname.slice(config.prefix.length) + current.search + current.hash;
    }
    return current.href;
  };
  const map = (value, websocket = false) => {
    const raw = String(value);
    if (!raw || raw.startsWith("#")) return raw;
    if (mounted(raw.split(/[?#]/, 1)[0])) return raw;
    let url;
    try { url = new URL(raw, upstreamBase()); } catch { return raw; }
    const local = new URL(location.href);
    if (websocket) {
      if (url.protocol === "ws:") url.protocol = "http:";
      if (url.protocol === "wss:") url.protocol = "https:";
    }
    if (url.origin === local.origin && mounted(url.pathname)) return raw;
    // Libraries frequently form absolute URLs using window.location.origin.
    if (![config.source, config.target, local.origin].includes(url.origin)) return raw;
    const path = config.prefix + url.pathname + url.search + url.hash;
    return websocket ? (local.protocol === "https:" ? "wss://" : "ws://") + local.host + path : path;
  };
  if (window.fetch) {
    const native = window.fetch;
    const remapRequest = async (input, mapped) => {
      // Passing a Request as RequestInit turns its body into a streaming
      // upload, which Chromium rejects on HTTP/1. Buffer only this overload,
      // with an explicit cap; ordinary fetch(url, {body}) stays untouched.
      const options = {};
      for (const key of ["method", "headers", "mode", "credentials", "cache", "redirect", "referrer", "referrerPolicy", "integrity", "keepalive", "signal"]) options[key] = input[key];
      if (input.body !== null) {
        const reader = input.body.getReader();
        const chunks = [];
        let size = 0;
        const abort = () => { void reader.cancel(input.signal.reason); };
        input.signal.addEventListener("abort", abort, {once:true});
        try {
          for (;;) {
            input.signal.throwIfAborted();
            const {done, value} = await reader.read();
            input.signal.throwIfAborted();
            if (done) break;
            size += value.byteLength;
            if (size > 16 * 1024 * 1024) {
              await reader.cancel();
              throw new TypeError("Pogo compatibility: Request body exceeds 16 MiB. Use fetch(url, init) or original forwarding.");
            }
            chunks.push(value);
          }
        } finally {
          input.signal.removeEventListener("abort", abort);
          reader.releaseLock();
        }
        const body = new Uint8Array(size);
        let offset = 0;
        for (const chunk of chunks) { body.set(chunk, offset); offset += chunk.byteLength; }
        options.body = body;
      }
      return native.call(window, new Request(new URL(mapped, location.href), options));
    };
    window.fetch = function(input, init) {
      if (input instanceof Request) {
        const mapped = map(input.url);
        if (new URL(mapped, location.href).href !== input.url) return remapRequest(init === undefined ? input : new Request(input, init), mapped);
      } else { input = map(input); }
      return native.call(this, input, init);
    };
  }
  const open = XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open = function(method, url, ...args) { return open.call(this, method, map(url), ...args); };
  for (const key of ["WebSocket", "EventSource"]) {
    const Native = window[key];
    if (!Native) continue;
    window[key] = new Proxy(Native, {
      construct(target, args, newTarget) {
        args[0] = map(args[0], key === "WebSocket");
        return Reflect.construct(target, args, newTarget);
      }
    });
  }
  for (const key of ["pushState", "replaceState"]) {
    const native = history[key];
    history[key] = function(state, title, url) {
      return native.call(this, state, title, url == null ? url : map(url));
    };
  }
  if (navigator.sendBeacon) {
    const native = navigator.sendBeacon;
    navigator.sendBeacon = function(url, data) { return native.call(this, map(url), data); };
  }
  const attrs = new Set(["src", "href", "action", "formaction", "poster", "data"]);
  const setAttribute = Element.prototype.setAttribute;
  Element.prototype.setAttribute = function(name, value) {
    return setAttribute.call(this, name, attrs.has(String(name).toLowerCase()) ? map(value) : value);
  };
  for (const [type, names] of [
    [HTMLScriptElement, ["src"]], [HTMLLinkElement, ["href"]],
    [HTMLImageElement, ["src"]], [HTMLIFrameElement, ["src"]],
    [HTMLAnchorElement, ["href"]], [HTMLFormElement, ["action"]],
    [HTMLInputElement, ["src", "formAction"]], [HTMLButtonElement, ["formAction"]],
    [HTMLMediaElement, ["src"]], [HTMLVideoElement, ["poster"]],
    [HTMLSourceElement, ["src"]], [HTMLObjectElement, ["data"]]
  ]) {
    for (const name of names) {
      const descriptor = Object.getOwnPropertyDescriptor(type.prototype, name);
      if (!descriptor?.set || !descriptor.configurable) continue;
      Object.defineProperty(type.prototype, name, { ...descriptor, set(value) { descriptor.set.call(this, map(value)); } });
    }
  }
  // Script-created cookies need the same mount scope as server Set-Cookie.
  const cookie = Object.getOwnPropertyDescriptor(Document.prototype, "cookie");
  if (cookie?.set && cookie.configurable) {
    Object.defineProperty(Document.prototype, "cookie", { ...cookie, set(value) {
      const parts = String(value).split(";");
      const name = parts[0].split("=", 1)[0].trim();
      if (name === "pi_session" || name.startsWith("__Host-")) {
        console.warn("[Pogo panel] This cookie requires an independent browser origin.");
        return;
      }
      for (let i = parts.length - 1; i > 0; i--) {
        const split = parts[i].indexOf("=");
        const key = parts[i].slice(0, split < 0 ? undefined : split).trim().toLowerCase();
        if (key === "domain") parts.splice(i, 1);
        else if (key === "path") {
          const path = parts[i].slice(split + 1).trim();
          if (path.startsWith("/") && !mounted(path)) parts[i] = "Path=" + config.prefix + path;
        }
      }
      cookie.set.call(this, parts.join(";"));
    } });
  }
})();
