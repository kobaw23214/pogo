// W3C Trace Context propagation over MCP. MCP 2026-07-28 carries
// traceparent/tracestate unprefixed in params._meta of each JSONRPC request.
// Only those two keys are accepted or emitted: baggage, identity, session and
// task claims in _meta are ignored by design.
package mcpobserve

import (
	"context"
	"strings"
)

// SpanIDFor maps a valid local 32-hex event id to its W3C span id: the first
// 16 lowercase hex digits, rejected when they would be the all-zero span id.
func SpanIDFor(id string) string {
	if len(id) != 32 {
		return ""
	}
	for i := 0; i < 32; i++ {
		if !isHexDigit(id[i]) {
			return ""
		}
	}
	span := strings.ToLower(id[:16])
	if span == "0000000000000000" {
		return ""
	}
	return span
}

// WithIncomingTrace re-derives tracing for one incoming protocol call from
// that call's own params._meta. Every call resets stale trace fields and the
// local ParentID first, so a missing or invalid traceparent starts fresh at
// the next Start and never reuses handshake-era or previous-call tracing.
// Unrelated trusted metadata already on the context (session, run, task,
// owner, client identity, entrypoint) is preserved; meta can only carry
// traceparent/tracestate and nothing else is read from it.
func WithIncomingTrace(ctx context.Context, meta map[string]any) context.Context {
	m := MetadataFrom(ctx)
	m.TraceID, m.ParentID, m.ParentSpanID, m.TraceFlags, m.TraceState = "", "", "", "", ""
	tp, _ := meta["traceparent"].(string)
	if traceID, spanID, flags, ok := parseTraceparent(tp); ok {
		m.TraceID, m.ParentSpanID, m.TraceFlags = traceID, spanID, flags
		if ts, _ := meta["tracestate"].(string); ts != "" && validTracestate(ts) {
			m.TraceState = ts
		}
	}
	return WithMetadata(ctx, m)
}

// TraceMeta returns a fresh map with the current traceparent and, when
// present and valid, a bounded tracestate — the keys a caller merges into an
// outbound params._meta without mutating any shared or request-owned map.
// It returns nil when there is no usable active parent span, so callers
// leave _meta untouched rather than inventing a trace.
func TraceMeta(ctx context.Context) map[string]any {
	m := MetadataFrom(ctx)
	if !validTraceID(m.TraceID) || !validSpanID(m.ParentSpanID) {
		return nil
	}
	flags := normTraceFlags(m.TraceFlags)
	meta := map[string]any{"traceparent": "00-" + m.TraceID + "-" + m.ParentSpanID + "-" + flags}
	if m.TraceState != "" && validTracestate(m.TraceState) {
		meta["tracestate"] = m.TraceState
	}
	return meta
}

// parseTraceparent validates a W3C traceparent strictly: version 00 must be
// exactly 55 lowercase chars in four fields; other versions (never ff) must
// have at least the four fields; both ids must be nonzero lowercase hex.
func parseTraceparent(s string) (traceID, spanID, flags string, ok bool) {
	if len(s) < 55 || len(s) > 1024 {
		return "", "", "", false
	}
	parts := strings.Split(s, "-")
	if len(parts) < 4 {
		return "", "", "", false
	}
	version := parts[0]
	if len(version) != 2 || !isLowerHex(version) || version == "ff" {
		return "", "", "", false
	}
	if version == "00" && (len(s) != 55 || len(parts) != 4) {
		return "", "", "", false
	}
	traceID, spanID, flags = parts[1], parts[2], parts[3]
	if !validTraceID(traceID) || !validSpanID(spanID) || len(flags) != 2 || !isLowerHex(flags) {
		return "", "", "", false
	}
	return traceID, spanID, flags, true
}

// validTracestate bounds and validates a W3C tracestate value: at most 32
// members, 512 bytes total, no duplicate keys, and keys/values inside the
// spec's character grammar.
func validTracestate(s string) bool {
	if len(s) > 512 {
		return false
	}
	members := strings.Split(s, ",")
	if len(members) > 32 {
		return false
	}
	seen := make(map[string]bool, len(members))
	for _, member := range members {
		// W3C permits surrounding OWS and empty list members. Whitespace
		// inside keys or values still follows their separate grammar.
		member = strings.Trim(member, " \t")
		if member == "" {
			continue
		}
		eq := strings.IndexByte(member, '=')
		if eq <= 0 {
			return false
		}
		key, value := member[:eq], member[eq+1:]
		if !validTracestateKey(key) || !validTracestateValue(value) || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

func validTracestateKey(key string) bool {
	if len(key) == 0 || len(key) > 256 {
		return false
	}
	if at := strings.IndexByte(key, '@'); at >= 0 {
		// Multi-tenant form: tenant may start with a digit, system may not.
		return validTracestatePart(key[:at], 241, true) && validTracestatePart(key[at+1:], 14, false)
	}
	return validTracestatePart(key, 256, false)
}

func validTracestatePart(s string, max int, digitStart bool) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	if !(s[0] >= 'a' && s[0] <= 'z') && !(digitStart && s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
			if i == 0 && !digitStart {
				return false
			}
		case c == '_' || c == '-' || c == '*' || c == '/':
		default:
			return false
		}
	}
	return true
}

func validTracestateValue(v string) bool {
	if len(v) == 0 || len(v) > 256 || v[len(v)-1] == ' ' {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c < 0x20 || c > 0x7e || c == ',' || c == '=' {
			return false
		}
	}
	return true
}

func validTraceID(id string) bool {
	return len(id) == 32 && isLowerHex(id) && id != "00000000000000000000000000000000"
}

func validSpanID(id string) bool {
	return len(id) == 16 && isLowerHex(id) && id != "0000000000000000"
}

// normTraceFlags keeps an already-valid flags byte (sampled=00 included) and
// defaults anything unset or malformed to 01.
func normTraceFlags(flags string) string {
	if len(flags) != 2 || !isLowerHex(flags) {
		return "01"
	}
	return flags
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return len(s) > 0
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
