// URL() intentionally accepts some host strings that cannot be DNS hostnames
// (including ASCII/full-width commas). Validate its IDNA-normalized hostname,
// without rewriting the address or imposing a protocol preference.
function validHostname(hostname: string): boolean {
  // URL parsing has already validated and normalized a bracketed IPv6 literal.
  if (hostname.startsWith("[") && hostname.endsWith("]")) return true;
  const host = hostname.endsWith(".") ? hostname.slice(0, -1) : hostname;
  return host.length > 0 && host.length <= 253 && host.split(".").every(label => label.length <= 63 && /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/i.test(label));
}

// Address-bar parsing shared by native navigation and bookmarks.
export function normalizeUrl(input: string, base: string): string | null {
  const s = input.trim();
  if (!s) return null;

  let candidate = s;
  if (!s.startsWith("/") && !s.startsWith("#")) {
    const m = s.match(/^([a-zA-Z][\w+.-]*):(.*)$/);
    if (!m) {
      candidate = `http://${s}`;
    } else if (m[1].toLowerCase() !== "http" && m[1].toLowerCase() !== "https") {
      // javascript:, data:, file:, about:, blob: are refused outright; anything
      // else ("localhost:8080", "sample-lib:77") is host:port, not a scheme.
      if (/^(javascript|data|file|vbscript|blob|about)$/i.test(m[1])) return null;
      candidate = `http://${s}`;
    }
  }
  try {
    const u = new URL(candidate, base);
    if ((u.protocol !== "http:" && u.protocol !== "https:") || !validHostname(u.hostname)) return null;
    return u.href;
  } catch {
    return null;
  }
}
